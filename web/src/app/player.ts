import { Chunk, ChunkType, codecFromParameterSets } from "./transport";
import { RestartBudget } from "./restart";

const AUDIO_BUFFER_MIN_S = 0.04;
const AUDIO_BUFFER_MAX_S = 0.2;
const AUDIO_JITTER_K = 2.5;
const AUDIO_RESYNC_S = 0.05;
const VIDEO_OFFSET_ALPHA = 0.05;
const VIDEO_OFFSET_SAMPLES = 10;
const VIDEO_JITTER_ALPHA = 0.05;
const VIDEO_JITTER_K = 3;
const VIDEO_LEAD_MAX_S = 0.15;
const VIDEO_QUEUE_MAX = 8;
const PACE_SWEEP_MS = 32;
const VIDEO_RESTART_LIMIT = 5;
const VIDEO_RESTART_WINDOW_MS = 60000;
const ATTEMPT_HISTORY_MAX = 8;

export class Player {
  private ctx: CanvasRenderingContext2D;
  private video: VideoDecoder | null = null;
  private audio: AudioDecoder | null = null;
  private audioCtx: AudioContext | null = null;
  private nextAudioAt = 0;
  private started = false;
  private videoOffset = 0;
  private videoSamples = 0;
  private arrivalOffset = 0;
  private ptsDelta = 0;
  private lastPts = -1;
  private queue: Array<{ frame: VideoFrame; at: number }> = [];
  private paceHandle = 0;
  private refresh = 1 / 60;
  private lastRaf = 0;
  private paceTimer = 0;
  private audioLocked = false;
  private skewCaptured = false;
  private audioArrival = 0;
  private audioSamples = 0;
  private audioNeed = AUDIO_BUFFER_MIN_S;
  private restarts = new RestartBudget(VIDEO_RESTART_LIMIT, VIDEO_RESTART_WINDOW_MS);

  decoded = 0;
  dropped = 0;
  codec = "";
  audioPlayed = 0;
  audioDropped = 0;
  audioState = "locked";
  audioResyncs = 0;
  audioUnderruns = 0;
  audioJitterMs = 0;
  audioBufferMs = AUDIO_BUFFER_MIN_S * 1000;
  videoJitterMs = 0;
  sourceJitterMs = 0;
  videoLeadMs = 0;
  videoDropped = 0;
  syncSkewMs = 0;
  audioOffsetMs = 0;
  videoOffsetMs = 0;
  videoRestarts = 0;

  constructor(
    private canvas: HTMLCanvasElement,
    private onError: (message: string) => void,
    private onAudioError: (message: string) => void = () => {},
    private onRecover: (message: string) => void = () => {},
  ) {
    const ctx = canvas.getContext("2d", { alpha: false });
    if (!ctx) throw new Error("2d canvas unavailable");
    this.ctx = ctx;
  }

  async unlockAudio(): Promise<string> {
    if (!this.audioCtx) {
      this.audioCtx = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
    }
    if (this.audioCtx.state === "suspended") {
      try {
        await this.audioCtx.resume();
      } catch (err) {
        this.onAudioError(`audio resume rejected: ${err}`);
      }
    }
    this.audioState = this.audioCtx.state;
    return this.audioState;
  }

  push(chunk: Chunk): void {
    switch (chunk.type) {
      case ChunkType.VideoConfig:
        void this.configureVideo(chunk);
        return;
      case ChunkType.VideoKey:
        if (!this.video) {
          void this.configureVideo(chunk);
          return;
        }
        this.decodeVideo(chunk);
        return;
      case ChunkType.VideoDelta:
        this.decodeVideo(chunk);
        return;
      case ChunkType.Audio:
        this.decodeAudio(chunk);
        return;
    }
  }

  private configuring = false;
  private candidates: VideoDecoderConfig[] = [];
  private candidateIndex = 0;
  private attempts: string[] = [];

  private async configureVideo(chunk: Chunk): Promise<void> {
    if (this.configuring) return;
    if (this.video && this.candidates.length > 0) return;

    this.configuring = true;
    try {
      const codecs = codecCandidates(chunk.payload);

      const shapes: Array<Partial<VideoDecoderConfig>> = [
        { codedWidth: this.canvas.width, codedHeight: this.canvas.height, optimizeForLatency: true },
        { codedWidth: this.canvas.width, codedHeight: this.canvas.height },
        { optimizeForLatency: true },
        {},
        { hardwareAcceleration: "prefer-software" },
      ];

      const viable: VideoDecoderConfig[] = [];
      for (const codec of codecs) {
        for (const shape of shapes) {
          const config = { codec, ...shape } as VideoDecoderConfig;
          try {
            const result = await VideoDecoder.isConfigSupported(config);
            if (result.supported) viable.push(config);
          } catch {
            // unsupported combinations simply do not join the list
          }
        }
      }

      if (viable.length === 0) {
        this.onError(`no video config accepted by this client (tried ${codecs.join(", ")})`);
        return;
      }

      this.candidates = viable;
      this.candidateIndex = 0;
      this.attempts = [];
      this.applyCandidate();
    } finally {
      this.configuring = false;
    }
  }

  private applyCandidate(): void {
    const config = this.candidates[this.candidateIndex];
    if (!config) {
      this.onError(`every H.264 config failed. tried: ${this.attempts.join(" | ")}`);
      return;
    }

    this.codec = describeConfig(config);

    try {
      this.video?.close();
    } catch {
      // closing an already-errored decoder is fine
    }

    this.video = new VideoDecoder({
      output: (frame) => this.onFrame(frame),
      error: (err) => this.onDecoderFailure(err),
    });

    try {
      this.video.configure(config);
      this.started = false;
    } catch (err) {
      this.attempts.push(`${this.codec} configure threw ${err}`);
      this.candidateIndex++;
      this.applyCandidate();
    }
  }

  private onDecoderFailure(err: unknown): void {
    this.attempts.push(`${this.codec} failed: ${err}`);
    while (this.attempts.length > ATTEMPT_HISTORY_MAX) this.attempts.shift();

    if (this.decoded > 0) {
      if (this.restarts.take(Date.now())) {
        this.videoRestarts++;
        this.drain();
        this.applyCandidate();
        this.onRecover(`video decoder restarted after ${err}`);
        return;
      }
      this.onError(`video decoder stopped: ${err}`);
      return;
    }

    this.candidateIndex++;
    if (this.candidateIndex < this.candidates.length) {
      this.applyCandidate();
      return;
    }
    this.onError(`every H.264 config failed. tried: ${this.attempts.join(" | ")}`);
  }

  private decodeVideo(chunk: Chunk): void {
    if (!this.video || this.video.state !== "configured") return;
    if (!this.started && chunk.type !== ChunkType.VideoKey) return;
    this.started = true;

    try {
      this.video.decode(
        new EncodedVideoChunk({
          type: chunk.type === ChunkType.VideoKey ? "key" : "delta",
          timestamp: chunk.pts,
          data: chunk.payload,
        }),
      );
    } catch (err) {
      this.started = false;
      this.onError(`decode: ${err}`);
    }
  }

  private audioFailed = false;

  private decodeAudio(chunk: Chunk): void {
    if (!this.audioCtx || this.audioFailed) return;

    if (!this.audio) {
      try {
        this.audio = new AudioDecoder({
          output: (data) => this.onAudio(data),
          error: (err) => this.failAudio(`audio decoder: ${err}`),
        });
        this.audio.configure({ codec: "opus", sampleRate: 48000, numberOfChannels: 2 });
      } catch (err) {
        this.failAudio(`audio unavailable: ${err}`);
        return;
      }
    }

    if (this.audio.state !== "configured") return;

    try {
      this.audio.decode(
        new EncodedAudioChunk({ type: "key", timestamp: chunk.pts, data: chunk.payload }),
      );
    } catch (err) {
      this.audioDropped++;
      if (this.audioDropped === 1) this.onAudioError(`audio packet rejected: ${err}`);
    }
  }

  private failAudio(message: string): void {
    if (this.audioFailed) return;
    this.audioFailed = true;
    this.onAudioError(message);
  }

  private onAudio(data: AudioData): void {
    const ctx = this.audioCtx;
    if (!ctx) {
      data.close();
      return;
    }

    try {
      const frames = data.numberOfFrames;
      const channels = Math.min(data.numberOfChannels, 2);
      const buffer = ctx.createBuffer(channels, frames, data.sampleRate);

      for (let ch = 0; ch < channels; ch++) {
        const plane = new Float32Array(frames);
        data.copyTo(plane, { planeIndex: ch, format: "f32-planar" });
        buffer.copyToChannel(plane, ch);
      }

      const source = ctx.createBufferSource();
      source.buffer = buffer;
      source.connect(ctx.destination);

      const now = ctx.currentTime;
      const stamp = data.timestamp / 1e6;
      const arrival = now - stamp;

      if (this.audioSamples === 0) {
        this.audioArrival = arrival;
      } else {
        const deviation = Math.abs(arrival - this.audioArrival);
        this.audioJitterMs += VIDEO_JITTER_ALPHA * (deviation * 1000 - this.audioJitterMs);
        this.audioArrival += VIDEO_OFFSET_ALPHA * (arrival - this.audioArrival);
      }
      this.audioSamples++;

      const bufferS = Math.min(
        AUDIO_BUFFER_MIN_S + (AUDIO_JITTER_K * this.audioJitterMs) / 1000,
        AUDIO_BUFFER_MAX_S,
      );
      this.audioBufferMs = bufferS * 1000;
      this.audioNeed = this.audioArrival + bufferS;

      const floor = now + bufferS;
      let start = this.nextAudioAt;

      if (start < now + 0.02) {
        start = floor;
        this.audioLocked = false;
        this.audioUnderruns++;
      }

      if (this.videoSamples >= VIDEO_OFFSET_SAMPLES) {
        const target = stamp + this.videoOffset;
        const adrift = !this.audioLocked || Math.abs(start - target) > AUDIO_RESYNC_S;
        if (adrift && target >= floor) {
          if (!this.skewCaptured) {
            this.syncSkewMs = (target - start) * 1000;
            this.skewCaptured = true;
          }
          start = target;
          this.audioLocked = true;
          this.audioResyncs++;
        }
      }

      this.audioOffsetMs = (start - stamp) * 1000;
      this.videoOffsetMs = this.videoOffset * 1000;

      source.start(start);
      this.nextAudioAt = start + buffer.duration;
      this.audioPlayed++;
    } catch (err) {
      this.failAudio(`audio render: ${err}`);
    } finally {
      data.close();
    }
  }

  private onFrame(frame: VideoFrame): void {
    this.decoded++;

    const pts = frame.timestamp / 1e6;
    this.trackSourcePacing(pts);

    const audio = this.audioCtx;
    if (!audio) {
      this.paint(frame);
      return;
    }

    const arrival = audio.currentTime - pts;

    if (this.videoSamples === 0) {
      this.arrivalOffset = arrival;
      this.videoOffset = arrival;
    } else {
      const deviation = Math.abs(arrival - this.arrivalOffset);
      this.arrivalOffset += VIDEO_OFFSET_ALPHA * (arrival - this.arrivalOffset);
      this.videoJitterMs += VIDEO_JITTER_ALPHA * (deviation * 1000 - this.videoJitterMs);

      const lead = Math.min((VIDEO_JITTER_K * this.videoJitterMs) / 1000, VIDEO_LEAD_MAX_S);
      const paced = this.arrivalOffset + lead;
      this.videoOffset = this.audioSamples > 0 ? Math.max(paced, this.audioNeed) : paced;
      this.videoLeadMs = (this.videoOffset - this.arrivalOffset) * 1000;
    }
    this.videoSamples++;

    this.queue.push({ frame, at: pts + this.videoOffset });
    while (this.queue.length > VIDEO_QUEUE_MAX) {
      const stale = this.queue.shift();
      stale?.frame.close();
      this.videoDropped++;
    }

    this.pace();
  }

  private trackSourcePacing(pts: number): void {
    if (this.lastPts >= 0) {
      const delta = pts - this.lastPts;
      if (this.ptsDelta === 0) {
        this.ptsDelta = delta;
      } else {
        this.sourceJitterMs += VIDEO_JITTER_ALPHA * (Math.abs(delta - this.ptsDelta) * 1000 - this.sourceJitterMs);
        this.ptsDelta += VIDEO_OFFSET_ALPHA * (delta - this.ptsDelta);
      }
    }
    this.lastPts = pts;
  }

  private pace(): void {
    if (!this.paceHandle) this.paceHandle = requestAnimationFrame((ts) => this.onRaf(ts));
    if (!this.paceTimer) this.paceTimer = window.setTimeout(() => this.onSweep(), PACE_SWEEP_MS);
  }

  private onRaf(ts: number): void {
    this.paceHandle = 0;

    if (this.lastRaf > 0) {
      const gap = (ts - this.lastRaf) / 1000;
      if (gap > 0.002 && gap < 0.1) this.refresh += 0.1 * (gap - this.refresh);
    }
    this.lastRaf = ts;

    const audio = this.audioCtx;
    if (!audio) {
      this.drain();
      return;
    }

    this.flush(audio.currentTime + this.refresh / 2);
    if (this.queue.length > 0) this.paceHandle = requestAnimationFrame((next) => this.onRaf(next));
  }

  private onSweep(): void {
    this.paceTimer = 0;

    const audio = this.audioCtx;
    if (!audio) {
      this.drain();
      return;
    }

    this.flush(audio.currentTime - this.refresh);
    if (this.queue.length > 0) this.paceTimer = window.setTimeout(() => this.onSweep(), PACE_SWEEP_MS);
  }

  private flush(horizon: number): void {
    let due: VideoFrame | null = null;

    while (this.queue.length > 0 && this.queue[0].at <= horizon) {
      const item = this.queue.shift() as { frame: VideoFrame; at: number };
      if (due) {
        due.close();
        this.videoDropped++;
      }
      due = item.frame;
    }

    if (due) this.paint(due);
  }

  private paint(frame: VideoFrame): void {
    try {
      this.ctx.drawImage(frame, 0, 0, this.canvas.width, this.canvas.height);
    } finally {
      frame.close();
    }
  }

  private drain(): void {
    for (const item of this.queue) item.frame.close();
    this.queue = [];
  }

  resize(width: number, height: number): void {
    if (this.canvas.width === width && this.canvas.height === height) return;
    this.canvas.width = width;
    this.canvas.height = height;
  }

  close(): void {
    if (this.paceHandle) cancelAnimationFrame(this.paceHandle);
    if (this.paceTimer) clearTimeout(this.paceTimer);
    this.paceHandle = 0;
    this.paceTimer = 0;
    this.drain();
    this.video?.close();
    this.audio?.close();
    void this.audioCtx?.close();
  }
}

function isAnnexB(payload: Uint8Array): boolean {
  return payload.length > 4 && payload[0] === 0 && payload[1] === 0 && payload[2] === 0 && payload[3] === 1;
}

function codecCandidates(payload: Uint8Array): string[] {
  if (!isAnnexB(payload)) {
    const named = new TextDecoder().decode(payload).trim();
    if (named === "vp8") return ["vp8"];
    if (named === "vp9") return ["vp09.00.10.08", "vp9"];
    if (named) return [named];
  }

  const parsed = codecFromParameterSets(payload);
  return [parsed, "avc1.64001f", "avc1.640028", "avc1.4d401f", "avc1.42e01f"]
    .filter((c): c is string => Boolean(c))
    .filter((c, i, all) => all.indexOf(c) === i);
}

function describeConfig(c: VideoDecoderConfig): string {
  const bits = [c.codec];
  if (c.codedWidth) bits.push(`${c.codedWidth}x${c.codedHeight}`);
  if (c.optimizeForLatency) bits.push("lowlat");
  if (c.hardwareAcceleration) bits.push(String(c.hardwareAcceleration));
  return bits.join("/");
}

export async function decodeSupported(): Promise<boolean> {
  if (typeof VideoDecoder === "undefined") return false;
  for (const codec of ["vp8", "avc1.640028", "avc1.42e028", "avc1.4d0028"]) {
    try {
      const support = await VideoDecoder.isConfigSupported({ codec, codedWidth: 1280, codedHeight: 720 });
      if (support.supported) return true;
    } catch {
      // try the next candidate profile
    }
  }
  return false;
}
