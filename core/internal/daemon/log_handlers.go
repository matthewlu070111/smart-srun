package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
)

// The two channels spec 03 fixes.
//
// "plugin" is everything this service recorded; "network" is the subset about
// the line. Which events are which is decided by the catalogue, not by the
// page: a filter maintained in the interface would go stale the first time an
// event was added here.
const (
	ChannelPlugin  = "plugin"
	ChannelNetwork = "network"
)

// LogTailParams selects records.
type LogTailParams struct {
	Channel string `json:"channel,omitempty"`
	// Cursor returns only what arrived after it. Zero starts from the oldest
	// record still held.
	Cursor uint64 `json:"cursor,omitempty"`
	// Since is a Unix second. The progress dialog polls with the moment it
	// submitted its action, so that it shows that action's lines and not the
	// ones from before the user pressed the button.
	Since int64 `json:"since,omitempty"`
	Lines int   `json:"lines,omitempty"`
}

// LogTailResult is one page.
type LogTailResult struct {
	Channel string   `json:"channel"`
	Lines   []string `json:"lines"`
	Cursor  uint64   `json:"cursor"`
	// Dropped says records were lost between the caller's cursor and the oldest
	// one still held, so a gap in a progress dialog can be explained rather
	// than looking like nothing happened.
	Dropped bool `json:"dropped,omitempty"`
}

func decodeChannel(name string) (string, error) {
	switch name {
	case "", ChannelPlugin:
		return ChannelPlugin, nil
	case ChannelNetwork:
		return ChannelNetwork, nil
	default:
		return "", domain.Errorf(domain.CodeInvalidArgument,
			"未知日志通道 %q，只有 plugin 和 network", name)
	}
}

func (d *Daemon) logTail(_ context.Context, raw json.RawMessage) (any, error) {
	var params LogTailParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	channel, err := decodeChannel(params.Channel)
	if err != nil {
		return nil, err
	}
	if params.Lines < 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "lines 不能为负")
	}

	query := logstore.TailQuery{Cursor: params.Cursor, Limit: params.Lines}
	if params.Since > 0 {
		query.Since = time.Unix(params.Since, 0)
	}
	if channel == ChannelNetwork {
		query.Events = logstore.NetworkEvents()
	}
	page := d.events.Tail(query)
	// An explicit empty slice: a JSON null here would make every caller check
	// for it before iterating, and one of them would forget.
	if page.Lines == nil {
		page.Lines = []string{}
	}
	return LogTailResult{Channel: channel, Lines: page.Lines,
		Cursor: page.Cursor, Dropped: page.Dropped}, nil
}

// LogDownloadParams bounds a full read.
type LogDownloadParams struct {
	Channel string `json:"channel,omitempty"`
	Lines   int    `json:"lines,omitempty"`
}

// LogDownloadResult carries the file as text, because that is what the browser
// saves and what a person reads.
type LogDownloadResult struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

func (d *Daemon) logDownload(_ context.Context, raw json.RawMessage) (any, error) {
	var params LogDownloadParams
	if err := control.DecodeParams(raw, &params); err != nil {
		return nil, err
	}
	channel, err := decodeChannel(params.Channel)
	if err != nil {
		return nil, err
	}
	if params.Lines < 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "lines 不能为负")
	}
	// The network channel is a filtered view of the same records, and the
	// filter lives in memory. A download of it is therefore the memory window,
	// not the file -- which is honest: the file has no marker saying which of
	// its lines were about the line.
	if channel == ChannelNetwork {
		page := d.events.Tail(logstore.TailQuery{
			Limit: params.Lines, Events: logstore.NetworkEvents()})
		return LogDownloadResult{Channel: channel,
			Text: strings.Join(page.Lines, "\n")}, nil
	}

	text, err := d.events.Download(params.Lines)
	if err != nil {
		return nil, err
	}
	return LogDownloadResult{Channel: channel, Text: text}, nil
}

// LogClearResult reports what a clear left behind: nothing, plus the cursor a
// reader should continue from so it does not ask for records that are gone.
type LogClearResult struct {
	Cursor uint64 `json:"cursor"`
}

func (d *Daemon) logClear(_ context.Context, raw json.RawMessage) (any, error) {
	if err := control.DecodeParams(raw, &struct{}{}); err != nil {
		return nil, err
	}
	if err := d.events.Clear(); err != nil {
		return nil, err
	}
	// Written after the clear, so the log a user just emptied is not empty: it
	// says who emptied it and when. An empty page and a cleared one are
	// different states, and only one of them means "nothing has happened".
	d.log(logstore.EventLogCleared, "")
	return LogClearResult{Cursor: d.events.Tail(logstore.TailQuery{}).Cursor}, nil
}
