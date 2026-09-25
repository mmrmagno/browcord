package capture

import (
	"context"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

func (p *Pipeline) WatchBus(ctx context.Context, report func(gst.MessageType, string)) {
	bus := p.pipeline.GetBus()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				msg := bus.PopFiltered(gst.MessageError | gst.MessageWarning | gst.MessageEOS)
				if msg == nil {
					break
				}
				report(msg.Type(), msg.String())
			}
		}
	}
}
