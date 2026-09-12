package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mmrmagno/browcord/internal/capture"
	"github.com/mmrmagno/browcord/internal/h264"
	"github.com/mmrmagno/browcord/internal/wire"
)

func main() {
	source := flag.String("source", "test", "capture source: x11 or test")
	display := flag.String("display", ":0", "X display when source is x11")
	out := flag.String("out", "capture.bcs", "output chunk stream")
	seconds := flag.Int("seconds", 10, "how long to capture")
	width := flag.Int("width", 1280, "capture width")
	height := flag.Int("height", 720, "capture height")
	fps := flag.Int("fps", 30, "frame rate")
	bitrate := flag.Int("bitrate", 4000, "video bitrate in kbps")
	audio := flag.Bool("audio", false, "also capture audio from pulse")
	audioDevice := flag.String("audio-device", "browcord.monitor", "pulse source")
	flag.Parse()

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer f.Close()

	var (
		mu        sync.Mutex
		counts    = map[wire.Type]int{}
		bytes     int
		firstPTS  uint64
		lastPTS   uint64
		havePTS   bool
		codec     string
		writeFail error
	)

	cfg := capture.Config{
		Source:           capture.Source(*source),
		Display:          *display,
		Width:            *width,
		Height:           *height,
		FPS:              *fps,
		VideoBitrateKbps: *bitrate,
		Audio:            *audio,
		AudioDevice:      *audioDevice,
	}

	log.Printf("pipeline: %s", capture.Description(cfg))

	pipeline, err := capture.New(cfg, func(c wire.Chunk) {
		mu.Lock()
		defer mu.Unlock()

		if writeFail != nil {
			return
		}
		if err := wire.WriteChunk(f, c); err != nil {
			writeFail = err
			return
		}

		counts[c.Type]++
		bytes += len(c.Payload)
		if c.Type == wire.VideoConfig {
			codec = h264.CodecString(c.Payload)
		}
		if c.Type != wire.VideoConfig {
			if !havePTS {
				firstPTS = c.PTS
				havePTS = true
			}
			lastPTS = c.PTS
		}
	})
	if err != nil {
		log.Fatalf("capture: %v", err)
	}

	if err := pipeline.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case <-time.After(time.Duration(*seconds) * time.Second):
	case <-stop:
	}

	pipeline.Stop()
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if writeFail != nil {
		log.Fatalf("write: %v", writeFail)
	}

	span := float64(lastPTS-firstPTS) / 1e6
	frames := counts[wire.VideoKey] + counts[wire.VideoDelta]

	fmt.Printf("\n%s\n", *out)
	fmt.Printf("  config chunks : %d (codec %s)\n", counts[wire.VideoConfig], codec)
	fmt.Printf("  keyframes     : %d\n", counts[wire.VideoKey])
	fmt.Printf("  delta frames  : %d\n", counts[wire.VideoDelta])
	fmt.Printf("  audio packets : %d\n", counts[wire.Audio])
	fmt.Printf("  pts span      : %.2fs\n", span)
	if span > 0 {
		fmt.Printf("  measured fps  : %.1f\n", float64(frames-1)/span)
		fmt.Printf("  video bitrate : %.2f Mbps\n", float64(bytes)*8/span/1e6)
	}
}
