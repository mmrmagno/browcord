package capture

import (
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/mmrmagno/browcord/internal/h264"
	"github.com/mmrmagno/browcord/internal/wire"
)

type Source string

const (
	SourceX11  Source = "x11"
	SourceTest Source = "test"
)

type Encoder string

const (
	EncoderAuto     Encoder = "auto"
	EncoderX264     Encoder = "x264enc"
	EncoderVAAPI    Encoder = "vah264enc"
	EncoderNVENC    Encoder = "nvh264enc"
	EncoderOpenH264 Encoder = "openh264enc"
	EncoderVP8      Encoder = "vp8enc"
)

var encoderPreference = []Encoder{EncoderVP8, EncoderX264, EncoderVAAPI, EncoderNVENC, EncoderOpenH264}

func (e Encoder) isH264() bool {
	return e != EncoderVP8
}

func (e Encoder) codecName() string {
	if e == EncoderVP8 {
		return "vp8"
	}
	return "h264"
}

type Config struct {
	Source           Source
	Display          string
	Width            int
	Height           int
	FPS              int
	VideoBitrateKbps int
	KeyframeSeconds  int
	Encoder          Encoder
	Audio            bool
	AudioDevice      string
	AudioBitrate     int
}

func (c *Config) applyDefaults() {
	if c.Source == "" {
		c.Source = SourceX11
	}
	if c.Display == "" {
		c.Display = ":0"
	}
	if c.Width == 0 {
		c.Width = 1920
	}
	if c.Height == 0 {
		c.Height = 1080
	}
	if c.FPS == 0 {
		c.FPS = 30
	}
	if c.VideoBitrateKbps == 0 {
		c.VideoBitrateKbps = 4000
	}
	if c.KeyframeSeconds == 0 {
		c.KeyframeSeconds = 1
	}
	if c.Encoder == "" {
		c.Encoder = EncoderAuto
	}
	if c.AudioDevice == "" {
		c.AudioDevice = "browcord.monitor"
	}
	if c.AudioBitrate == 0 {
		c.AudioBitrate = 128000
	}
}

type Sink func(wire.Chunk)

type timebase struct {
	mu   sync.Mutex
	base uint64
	set  bool
}

func (t *timebase) relative(pts uint64) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.set {
		t.base = pts
		t.set = true
	}
	if pts < t.base {
		return 0
	}
	return pts - t.base
}

type Pipeline struct {
	cfg      Config
	pipeline gst.Pipeline
	sink     Sink

	clock timebase

	mu         sync.Mutex
	sentConfig bool
}

var initOnce sync.Once

func ensureInit() {
	initOnce.Do(func() { gst.Init() })
}

func New(cfg Config, sink Sink) (*Pipeline, error) {
	if sink == nil {
		return nil, fmt.Errorf("capture: sink is required")
	}
	ensureInit()
	cfg.applyDefaults()

	element, err := gst.ParseLaunch(Description(cfg))
	if err != nil {
		return nil, fmt.Errorf("capture: build pipeline: %w", err)
	}

	pipeline, ok := element.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("capture: parsed description is not a pipeline")
	}

	p := &Pipeline{cfg: cfg, pipeline: pipeline, sink: sink}

	if err := p.attach("video", p.onVideo); err != nil {
		return nil, err
	}
	if cfg.Audio {
		if err := p.attach("audio", p.onAudio); err != nil {
			return nil, err
		}
	}

	return p, nil
}

func Description(cfg Config) string {
	ensureInit()
	cfg.applyDefaults()

	var source string
	switch cfg.Source {
	case SourceTest:
		source = fmt.Sprintf("videotestsrc is-live=true pattern=smpte ! video/x-raw,width=%d,height=%d,framerate=%d/1",
			cfg.Width, cfg.Height, cfg.FPS)
	default:
		source = fmt.Sprintf("ximagesrc display-name=%s use-damage=false show-pointer=false ! video/x-raw,framerate=%d/1",
			cfg.Display, cfg.FPS)
	}

	parts := []string{
		source,
		"videoconvert",
		"video/x-raw,format=I420",
		"queue max-size-buffers=1 max-size-time=0 max-size-bytes=0 leaky=downstream",
		encoderDescription(cfg),
	}

	if ResolveEncoder(cfg.Encoder).isH264() {
		parts = append(parts,
			"h264parse config-interval=-1",
			"video/x-h264,stream-format=byte-stream,alignment=au",
		)
	}

	parts = append(parts, "appsink name=video emit-signals=true sync=false max-buffers=1 drop=true")

	desc := strings.Join(parts, " ! ")

	if cfg.Audio {
		audio := []string{
			fmt.Sprintf("pulsesrc device=%s", cfg.AudioDevice),
			"audio/x-raw,rate=48000,channels=2",
			"audioconvert",
			"audioresample",
			"queue max-size-buffers=4 max-size-time=0 max-size-bytes=0 leaky=downstream",
			fmt.Sprintf("opusenc bitrate=%d frame-size=20", cfg.AudioBitrate),
			"appsink name=audio emit-signals=true sync=false max-buffers=8 drop=true",
		}
		desc += " " + strings.Join(audio, " ! ")
	}

	return desc
}

func ResolveEncoder(preferred Encoder) Encoder {
	ensureInit()

	if preferred != EncoderAuto && preferred != "" {
		return preferred
	}

	for _, candidate := range encoderPreference {
		if gst.ElementFactoryFind(string(candidate)) != nil {
			return candidate
		}
	}

	return EncoderX264
}

func encoderDescription(cfg Config) string {
	keyint := cfg.FPS * cfg.KeyframeSeconds

	switch ResolveEncoder(cfg.Encoder) {
	case EncoderVAAPI:
		return fmt.Sprintf("vah264enc bitrate=%d key-int-max=%d b-frames=0", cfg.VideoBitrateKbps, keyint)
	case EncoderNVENC:
		return fmt.Sprintf("nvh264enc preset=low-latency-hq bitrate=%d gop-size=%d bframes=0", cfg.VideoBitrateKbps, keyint)
	case EncoderVP8:
		return fmt.Sprintf(
			"vp8enc deadline=1 cpu-used=8 end-usage=cbr target-bitrate=%d keyframe-max-dist=%d "+
				"lag-in-frames=0 buffer-size=200 buffer-initial-size=150 buffer-optimal-size=175 "+
				"static-threshold=100 error-resilient=1 threads=4 token-partitions=2 ! video/x-vp8",
			cfg.VideoBitrateKbps*1000, keyint)
	case EncoderOpenH264:
		return fmt.Sprintf("openh264enc usage-type=screen bitrate=%d gop-size=%d", cfg.VideoBitrateKbps*1000, keyint)
	default:
		return fmt.Sprintf("x264enc tune=zerolatency speed-preset=veryfast bitrate=%d key-int-max=%d bframes=0",
			cfg.VideoBitrateKbps, keyint)
	}
}

func (p *Pipeline) attach(name string, handler func(gstapp.AppSink) gst.FlowReturn) error {
	element := p.pipeline.GetByName(name)
	if element == nil {
		return fmt.Errorf("capture: appsink %q not found in pipeline", name)
	}

	sink, ok := element.(gstapp.AppSink)
	if !ok {
		return fmt.Errorf("capture: element %q is not an appsink", name)
	}

	sink.ConnectNewSample(handler)
	return nil
}

func (p *Pipeline) Start() error {
	if ret := p.pipeline.SetState(gst.StatePlaying); ret == gst.StateChangeFailure {
		return fmt.Errorf("capture: pipeline refused to start")
	}
	return nil
}

func (p *Pipeline) Stop() {
	p.pipeline.SetState(gst.StateNull)
}

func (p *Pipeline) Bus() gst.Bus {
	return p.pipeline.GetBus()
}

func (p *Pipeline) onVideo(sink gstapp.AppSink) gst.FlowReturn {
	data, pts, keyframe, flow := pull(sink)
	if flow != gst.FlowOK {
		return flow
	}

	relative := p.clock.relative(pts)

	if keyframe {
		if payload := p.configPayload(data); len(payload) > 0 {
			p.mu.Lock()
			p.sentConfig = true
			p.mu.Unlock()
			p.sink(wire.Chunk{Type: wire.VideoConfig, PTS: relative, Payload: payload})
		}
	}

	chunkType := wire.VideoDelta
	if keyframe {
		chunkType = wire.VideoKey
	} else if !p.configSeen() {
		return gst.FlowOK
	}

	p.sink(wire.Chunk{Type: chunkType, PTS: relative, Payload: data})
	return gst.FlowOK
}

func (p *Pipeline) configPayload(frame []byte) []byte {
	if !ResolveEncoder(p.cfg.Encoder).isH264() {
		return []byte(ResolveEncoder(p.cfg.Encoder).codecName())
	}
	return h264.ParameterSets(h264.SplitAnnexB(frame))
}

func (p *Pipeline) configSeen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sentConfig
}

func (p *Pipeline) onAudio(sink gstapp.AppSink) gst.FlowReturn {
	data, pts, _, flow := pull(sink)
	if flow != gst.FlowOK {
		return flow
	}

	relative := p.clock.relative(pts)

	p.sink(wire.Chunk{Type: wire.Audio, PTS: relative, Payload: data})
	return gst.FlowOK
}

func pull(sink gstapp.AppSink) ([]byte, uint64, bool, gst.FlowReturn) {
	sample := sink.PullSample()
	if sample == nil {
		return nil, 0, false, gst.FlowEOS
	}

	buffer := sample.GetBuffer()
	if buffer == nil {
		return nil, 0, false, gst.FlowError
	}

	info, ok := buffer.Map(gst.MapRead)
	if !ok {
		return nil, 0, false, gst.FlowError
	}
	defer info.Unmap()

	data, err := io.ReadAll(info)
	if err != nil || len(data) == 0 {
		return nil, 0, false, gst.FlowError
	}

	var ptsMicros uint64
	if pts := uint64(buffer.PTS()); pts != math.MaxUint64 {
		ptsMicros = pts / 1000
	}

	return data, ptsMicros, !buffer.HasFlags(gst.BufferFlagDeltaUnit), gst.FlowOK
}
