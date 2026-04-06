package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/andusystems/sentinel/internal/types"
)

// EventProcessor routes dequeued events to the appropriate handlers.
// It runs N worker goroutines (config.webhook.processing_workers).
type EventProcessor struct {
	queue       *Queue
	prHandler   PREventHandler
	pushHandler PushEventHandler
	numWorkers  int
}

// PREventHandler handles pull_request and issue_comment webhook events.
type PREventHandler interface {
	HandlePREvent(ctx context.Context, event types.ForgejoEvent)
}

// PushEventHandler handles push webhook events (sentinel branch pushes only).
type PushEventHandler interface {
	HandlePushEvent(ctx context.Context, event types.ForgejoEvent)
}

// NewEventProcessor creates an EventProcessor with the given handlers.
func NewEventProcessor(queue *Queue, pr PREventHandler, push PushEventHandler, numWorkers int) *EventProcessor {
	return &EventProcessor{
		queue:       queue,
		prHandler:   pr,
		pushHandler: push,
		numWorkers:  numWorkers,
	}
}

// Start launches N worker goroutines and blocks until all exit.
// Workers exit when ctx is cancelled or the queue channel is closed.
func (p *EventProcessor) Start(ctx context.Context) {
	ch, _ := p.queue.Dequeue(ctx)

	var wg sync.WaitGroup
	for i := 0; i < p.numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case event, ok := <-ch:
					if !ok {
						return // queue closed on shutdown
					}
					p.dispatch(ctx, event, workerID)
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// dispatch routes a single event to the appropriate handler.
func (p *EventProcessor) dispatch(ctx context.Context, event types.ForgejoEvent, workerID int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("webhook processor panic",
				"worker", workerID, "event", event.Type, "repo", event.Repo, "panic", r)
		}
	}()

	switch event.Type {
	case "pull_request":
		if p.prHandler != nil {
			p.prHandler.HandlePREvent(ctx, event)
		}
	case "push":
		if p.pushHandler != nil {
			// Pass ALL pushes to the handler; it routes on branch name
			// (sentinel/* → open PR, default branch → trigger Mode 3 sync).
			p.pushHandler.HandlePushEvent(ctx, event)
		}
	case "issue_comment":
		if p.prHandler != nil && containsReviewCommand(event.Payload) {
			p.prHandler.HandlePREvent(ctx, event)
		}
	default:
		slog.Debug("webhook processor: unhandled event type", "type", event.Type)
	}
}

// extractHeadRef parses the head ref from a push webhook payload.
func extractHeadRef(payload []byte) string {
	var partial struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(payload, &partial); err != nil {
		return ""
	}
	return strings.TrimPrefix(partial.Ref, "refs/heads/")
}

// containsReviewCommand checks if an issue_comment payload body contains "/review".
func containsReviewCommand(payload []byte) bool {
	var partial struct {
		Comment struct {
			Body string `json:"body"`
		} `json:"comment"`
	}
	json.Unmarshal(payload, &partial)
	return strings.Contains(partial.Comment.Body, "/review")
}
