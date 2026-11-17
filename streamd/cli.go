package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"
)

type APIClient struct {
	http *http.Client
	base string
}

func newUnixClient(socket string) *APIClient {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &APIClient{
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second},
		base: "http://streamd",
	}
}

func newTCPClient(listen string) *APIClient {
	return &APIClient{
		http: &http.Client{Timeout: 30 * time.Second},
		base: "http://" + listen,
	}
}

func clientForConfig(cfg *Config) *APIClient {
	if cfg.Server.Socket != "" {
		return newUnixClient(cfg.Server.Socket)
	}
	return newTCPClient(cfg.Server.Listen)
}

func (c *APIClient) do(method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil && payload.Error != "" {
			return errors.New(payload.Error)
		}
		return fmt.Errorf("request failed with status %s", resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type globalFlags struct {
	cfgPath string
	envPath string
	json    bool
}

func addGlobalFlags(fs *flag.FlagSet, g *globalFlags) {
	fs.StringVar(&g.cfgPath, "config", defaultConfigPath, "configuration file")
	fs.StringVar(&g.envPath, "env", "", "env file (default: .env next to config)")
	fs.BoolVar(&g.json, "json", false, "emit JSON")
}

func loadCLIConfig(g *globalFlags) (*Config, error) {
	return loadConfig(g.cfgPath, g.envPath)
}

func cmdCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command")
	}
	switch args[0] {
	case "status":
		return cmdStatus(args[1:])
	case "stream":
		return cmdStream(args[1:])
	case "logs":
		return cmdLogs(args[1:])
	case "config":
		return cmdConfig(args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func cmdStatus(args []string) error {
	fs := newFlagSet("status")
	var g globalFlags
	addGlobalFlags(fs, &g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)

	var status struct {
		Version       string         `json:"version"`
		UptimeSeconds int            `json:"uptime_seconds"`
		Streams       int            `json:"streams"`
		Uploader      UploaderStatus `json:"uploader"`
	}
	if err := client.do("GET", "/control/status", nil, &status); err != nil {
		return err
	}
	var streams struct {
		Streams  []StreamView   `json:"streams"`
		Uploader UploaderStatus `json:"uploader"`
	}
	if err := client.do("GET", "/control/streams", nil, &streams); err != nil {
		return err
	}
	if g.json {
		return printJSON(map[string]any{
			"daemon":   status,
			"streams":  streams.Streams,
			"uploader": streams.Uploader,
		})
	}

	fmt.Printf("streamd %s  up %s\n", status.Version, time.Duration(status.UptimeSeconds)*time.Second)
	fmt.Printf("uploader: enabled=%s active=%d pending=%d uploading=%d failed=%d completed=%d\n",
		httpBool(streams.Uploader.Enabled), streams.Uploader.Active,
		streams.Uploader.Counts["pending"], streams.Uploader.Counts["uploading"],
		streams.Uploader.Counts["failed"], streams.Uploader.Counts["completed"])

	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tRUNNING\tSTOPPED\tENABLED\tLAST ACTIVITY")
	for _, s := range streams.Streams {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Name, s.Type, httpBool(s.Running), httpBool(s.Stopped), httpBool(s.Enabled), formatTimePtr(s.LastActivity))
	}
	return tw.Flush()
}

func cmdStream(args []string) error {
	if len(args) == 0 {
		return errors.New("missing stream command (list, show, add, update, remove, start, stop, restart)")
	}
	switch args[0] {
	case "list":
		return cmdStreamList(args[1:])
	case "show":
		return cmdStreamShow(args[1:])
	case "add":
		return cmdStreamAdd(args[1:])
	case "update":
		return cmdStreamUpdate(args[1:])
	case "remove":
		return cmdStreamAction(args[1:], "remove")
	case "start":
		return cmdStreamAction(args[1:], "start")
	case "stop":
		return cmdStreamAction(args[1:], "stop")
	case "restart":
		return cmdStreamAction(args[1:], "restart")
	}
	return fmt.Errorf("unknown stream command %q", args[0])
}

func cmdStreamList(args []string) error {
	fs := newFlagSet("stream list")
	var g globalFlags
	addGlobalFlags(fs, &g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)
	var streams struct {
		Streams  []StreamView   `json:"streams"`
		Uploader UploaderStatus `json:"uploader"`
	}
	if err := client.do("GET", "/control/streams", nil, &streams); err != nil {
		return err
	}
	if g.json {
		return printJSON(streams.Streams)
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tRUNNING\tSTOPPED\tENABLED\tLAST ACTIVITY")
	for _, s := range streams.Streams {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Name, s.Type, httpBool(s.Running), httpBool(s.Stopped), httpBool(s.Enabled), formatTimePtr(s.LastActivity))
	}
	return tw.Flush()
}

func cmdStreamShow(args []string) error {
	fs := newFlagSet("stream show")
	var g globalFlags
	addGlobalFlags(fs, &g)
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := firstArg(fs, "")
	if id == "" {
		return errors.New("stream id is required")
	}
	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)
	var view StreamView
	if err := client.do("GET", "/control/streams/"+url.PathEscape(id), nil, &view); err != nil {
		return err
	}
	if g.json {
		return printJSON(view)
	}
	fmt.Printf("ID:            %s\n", view.ID)
	fmt.Printf("Name:          %s\n", view.Name)
	fmt.Printf("Type:          %s\n", view.Type)
	fmt.Printf("URL:           %s\n", view.URL)
	fmt.Printf("Enabled:       %s\n", httpBool(view.Enabled))
	fmt.Printf("Running:       %s\n", httpBool(view.Running))
	fmt.Printf("Stopped:       %s\n", httpBool(view.Stopped))
	if view.Error != "" {
		fmt.Printf("Error:         %s\n", view.Error)
	}
	fmt.Printf("Chunk seconds: %d\n", view.ChunkSeconds)
	fmt.Printf("Last activity: %s\n", formatTimePtr(view.LastActivity))
	if view.Audio != nil {
		fmt.Printf("Audio:         codec=%s container=%s bitrate=%s rate=%d channels=%d\n",
			view.Audio.Codec, view.Audio.Container, view.Audio.Bitrate, view.Audio.SampleRate, view.Audio.Channels)
	}
	if len(view.Metadata) > 0 {
		fmt.Println("Metadata:")
		data, _ := json.MarshalIndent(view.Metadata, "  ", "  ")
		fmt.Println("  " + strings.ReplaceAll(string(data), "\n", "\n  "))
	}
	return nil
}

func cmdStreamAdd(args []string) error {
	fs := newFlagSet("stream add")
	var g globalFlags
	addGlobalFlags(fs, &g)
	id := fs.String("id", "", "stream id")
	name := fs.String("name", "", "display name")
	typ := fs.String("type", "", "source type")
	sourceURL := fs.String("url", "", "source url")
	enabled := fs.Bool("enabled", false, "start automatically")
	chunk := fs.Int("chunk-seconds", 0, "chunk duration override")
	file := fs.String("file", "", "stream YAML file ('-' for stdin)")
	var metadata stringListFlag
	fs.Var(&metadata, "metadata", "metadata key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		*id = firstArg(fs, "")
	}
	if *id == "" {
		return errors.New("stream id is required (positional or --id)")
	}

	var stream StreamConfig
	if *file != "" {
		data, err := readInput(*file)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &stream); err != nil {
			return err
		}
	} else {
		stream = StreamConfig{ID: *id, Name: *name, Type: *typ, URL: *sourceURL, Enabled: *enabled, ChunkSeconds: *chunk}
	}
	stream.ID = *id
	if stream.Name == "" {
		stream.Name = *name
	}
	if *typ != "" {
		stream.Type = *typ
	}
	if *sourceURL != "" {
		stream.URL = *sourceURL
	}
	if *enabled {
		stream.Enabled = true
	}
	if *chunk != 0 {
		stream.ChunkSeconds = *chunk
	}
	applyMetadataFlags(&stream, metadata)

	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)
	var out map[string]any
	if err := client.do("POST", "/control/streams", stream, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out)
	}
	fmt.Printf("stream %s added\n", stream.ID)
	if warnings, ok := out["warnings"].([]any); ok {
		for _, warning := range warnings {
			fmt.Printf("warning: %v\n", warning)
		}
	}
	return nil
}

func cmdStreamUpdate(args []string) error {
	fs := newFlagSet("stream update")
	var g globalFlags
	addGlobalFlags(fs, &g)
	name := fs.String("name", "", "display name")
	typ := fs.String("type", "", "source type")
	sourceURL := fs.String("url", "", "source url")
	enabled := fs.String("enabled", "", "true or false")
	chunk := fs.Int("chunk-seconds", 0, "chunk duration override")
	file := fs.String("file", "", "stream YAML file ('-' for stdin)")
	var metadata stringListFlag
	fs.Var(&metadata, "metadata", "metadata key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := firstArg(fs, "")
	if id == "" {
		return errors.New("stream id is required")
	}

	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)

	var stream StreamConfig
	if *file != "" {
		data, err := readInput(*file)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &stream); err != nil {
			return err
		}
	} else {
		var view StreamView
		if err := client.do("GET", "/control/streams/"+url.PathEscape(id), nil, &view); err != nil {
			return err
		}
		stream = streamViewToConfig(view)
	}
	stream.ID = id
	if *name != "" {
		stream.Name = *name
	}
	if *typ != "" {
		stream.Type = *typ
	}
	if *sourceURL != "" {
		stream.URL = *sourceURL
	}
	if *enabled != "" {
		stream.Enabled = strings.EqualFold(*enabled, "true")
	}
	if *chunk != 0 {
		stream.ChunkSeconds = *chunk
	}
	applyMetadataFlags(&stream, metadata)

	var out map[string]any
	if err := client.do("PUT", "/control/streams/"+url.PathEscape(id), stream, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out)
	}
	fmt.Printf("stream %s updated\n", id)
	return nil
}

func cmdStreamAction(args []string, action string) error {
	fs := newFlagSet("stream " + action)
	var g globalFlags
	addGlobalFlags(fs, &g)
	force := fs.Bool("force", false, "start even when the server is at capacity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := firstArg(fs, "")
	if id == "" {
		return errors.New("stream id is required")
	}
	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)
	method := "POST"
	path := "/control/streams/" + url.PathEscape(id) + "/" + action
	if action == "start" && *force {
		path += "?force=true"
	}
	if action == "remove" {
		method = "DELETE"
		path = "/control/streams/" + url.PathEscape(id)
	}
	var out map[string]any
	if err := client.do(method, path, nil, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out)
	}
	fmt.Printf("stream %s %s: ok\n", id, action)
	return nil
}

func cmdLogs(args []string) error {
	fs := newFlagSet("logs")
	var g globalFlags
	addGlobalFlags(fs, &g)
	level := fs.String("level", "", "minimum level (debug, info, warn, error)")
	search := fs.String("search", "", "substring filter")
	limit := fs.Int("limit", 200, "maximum entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	stream := firstArg(fs, "")

	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	client := clientForConfig(cfg)
	query := url.Values{}
	if stream != "" {
		query.Set("stream", stream)
	}
	if *level != "" {
		query.Set("level", *level)
	}
	if *search != "" {
		query.Set("search", *search)
	}
	query.Set("limit", fmt.Sprint(*limit))
	var out struct {
		Entries []LogEntry `json:"entries"`
	}
	if err := client.do("GET", "/control/logs?"+query.Encode(), nil, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out.Entries)
	}
	for _, e := range out.Entries {
		fmt.Printf("%s %-5s %-20s %s\n", e.Time.Format("2006-01-02T15:04:05Z07:00"), e.Level, e.Stream, e.Message)
	}
	return nil
}

func cmdConfig(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: streamd config validate")
	}
	fs := newFlagSet("config validate")
	var g globalFlags
	addGlobalFlags(fs, &g)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := loadCLIConfig(&g)
	if err != nil {
		return err
	}
	fmt.Printf("configuration OK: %d streams\n", len(cfg.Streams))
	for _, id := range sortedIDs(cfg) {
		eff, _ := cfg.FindStream(id)
		fmt.Printf("  %-24s type=%-8s enabled=%-5s chunk=%ds format=%s\n",
			id, eff.Type, httpBool(eff.Enabled), eff.ChunkSeconds, cfg.streamFormat(eff))
	}
	return nil
}

// ---------------------------------------------------------------------------
// CLI helpers
// ---------------------------------------------------------------------------

type stringListFlag []string

func (s *stringListFlag) String() string { return strings.Join(*s, ",") }
func (s *stringListFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func firstArg(fs *flag.FlagSet, def string) string {
	if fs.NArg() > 0 {
		return fs.Arg(0)
	}
	return def
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func applyMetadataFlags(stream *StreamConfig, values []string) {
	if len(values) == 0 {
		return
	}
	if stream.Metadata == nil {
		stream.Metadata = map[string]any{}
	}
	for _, pair := range values {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		stream.Metadata[k] = v
	}
}

func streamViewToConfig(v StreamView) StreamConfig {
	stream := StreamConfig{
		ID: v.ID, Name: v.Name, Type: v.Type, URL: v.URL,
		Enabled: v.Enabled, ChunkSeconds: v.ChunkSeconds,
		Audio: v.Audio, Video: v.Video, Metadata: v.Metadata,
	}
	return stream
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}
