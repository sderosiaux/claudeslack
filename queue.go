package main

import (
	"fmt"
	"sync"
)

// QueuedMessage represents a message waiting to be processed
type QueuedMessage struct {
	Text      string
	ChannelID string
	ThreadTS  string
	EventTS   string
	UserID    string
	WorkDir   string
	FilePaths []string
}

// ChannelQueue manages message queues per channel
type ChannelQueue struct {
	mu       sync.Mutex
	busy     map[string]bool                // channel -> is processing
	queues   map[string][]*QueuedMessage    // channel -> queued messages
	handlers map[string]func(*QueuedMessage) // channel -> handler function
}

// NewChannelQueue creates a new queue manager
func NewChannelQueue() *ChannelQueue {
	return &ChannelQueue{
		busy:     make(map[string]bool),
		queues:   make(map[string][]*QueuedMessage),
		handlers: make(map[string]func(*QueuedMessage)),
	}
}

// SetHandler sets the message handler for a channel
func (cq *ChannelQueue) SetHandler(channelID string, handler func(*QueuedMessage)) {
	cq.mu.Lock()
	defer cq.mu.Unlock()
	cq.handlers[channelID] = handler
}

// Submit submits a message for processing
// Returns: (isQueued bool, queuePosition int)
// isQueued=false means it will be processed immediately
// isQueued=true means it was added to queue, position is 1-indexed
func (cq *ChannelQueue) Submit(msg *QueuedMessage) (bool, int) {
	cq.mu.Lock()
	defer cq.mu.Unlock()

	if cq.busy[msg.ChannelID] {
		// Channel is busy, queue the message
		cq.queues[msg.ChannelID] = append(cq.queues[msg.ChannelID], msg)
		position := len(cq.queues[msg.ChannelID])
		return true, position
	}

	// Channel is free, mark as busy and process
	cq.busy[msg.ChannelID] = true
	return false, 0
}

// Done marks current processing as complete and processes next in queue
// Returns the next message to process, or nil if queue is empty
func (cq *ChannelQueue) Done(channelID string) *QueuedMessage {
	cq.mu.Lock()
	defer cq.mu.Unlock()

	queue := cq.queues[channelID]
	if len(queue) > 0 {
		// Get next message
		next := queue[0]
		cq.queues[channelID] = queue[1:]
		// Keep busy=true since we're processing next
		return next
	}

	// Queue empty, mark as free
	cq.busy[channelID] = false
	return nil
}

// QueueLength returns the current queue length for a channel
func (cq *ChannelQueue) QueueLength(channelID string) int {
	cq.mu.Lock()
	defer cq.mu.Unlock()
	return len(cq.queues[channelID])
}

// IsBusy returns whether a channel is currently processing
func (cq *ChannelQueue) IsBusy(channelID string) bool {
	cq.mu.Lock()
	defer cq.mu.Unlock()
	return cq.busy[channelID]
}

// GetQueueStatus returns a formatted status string for a channel
func (cq *ChannelQueue) GetQueueStatus(channelID string) string {
	cq.mu.Lock()
	defer cq.mu.Unlock()

	qLen := len(cq.queues[channelID])
	if !cq.busy[channelID] {
		return "idle"
	}
	if qLen == 0 {
		return "processing"
	}
	return fmt.Sprintf("processing + %d queued", qLen)
}
