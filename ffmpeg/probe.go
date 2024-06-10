package ffmpeg

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"time"
)

type Stream struct {
	Index         int    `json:"index"`
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	CodecLongName string `json:"codec_long_name"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FrameRate     string `json:"avg_frame_rate"`
}

type StreamProbe struct {
	Streams []Stream `json:"streams"`
}

func ProbeRTSP(log *slog.Logger, url string) ([]Stream, error) {
	ctx, _ := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", url)
	stout, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	log.Debug("ffprobe complete", slog.String("url", url), slog.String("stout", string(stout)))

	probe := &StreamProbe{}
	err = json.Unmarshal(stout, probe)
	if err != nil {
		return nil, err
	}
	return probe.Streams, nil
}
