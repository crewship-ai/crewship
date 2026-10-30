package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

const mooringCLIFrameLimit = 256 << 10

func newMooringConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <page-slug|@ID>",
		Short: "Exchange original collaboration frames over an authenticated connection",
		Long: `Open the authenticated WebSocket of a Mooring session and exchange its
original protocol frames. The server must run with CREWSHIP_MOORING_ENABLED
(off by default); otherwise the upgrade is refused. A human CLI credential is
required; an agent token is refused before any request.

Pass the connection_ref returned by 'crewship mooring session' with
--connection, and one original JSON frame per input line with --file (- reads
stdin). The first frame must be the original session_join with its one-use
ticket. Each frame is limited to 256 KiB.

Server frames print as JSON lines by default and with -f json or -f ndjson, or
as YAML documents with -f yaml; -f quiet is refused. A refused upgrade reports
"Mooring connection rejected" without the HTTP status. A pending frame is not a
saved change and this command creates no receipts: keep the original packets
for retries.`,
		Example: `  crewship mooring session my-doc --file session.json -f json
  crewship mooring connect my-doc --connection <connection-ref> --file frames.ndjson
  cat frames.ndjson | crewship mooring connect @<page-id> --connection <connection-ref> --file - -f yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := humanWorkbookClient()
			if err != nil {
				return err
			}
			return runMooringConnect(cmd, args[0], client)
		},
	}
	cmd.Flags().String("connection", "", "Original connection_ref returned by mooring session")
	cmd.Flags().String("file", "", "Original JSON frames, one per line; - reads stdin")
	return cmd
}

func runMooringConnect(cmd *cobra.Command, page string, client *cli.Client) error {
	formatter := resolvedFormatter(cmd)
	formatter.Writer = cmd.OutOrStdout()
	switch formatter.Format {
	case "table", "json", "ndjson", "yaml":
		// The default human mode retains the original JSON-lines interface.
	default:
		return errors.New("Mooring streams require json, ndjson or yaml output")
	}
	connection, _ := cmd.Flags().GetString("connection")
	inputPath, _ := cmd.Flags().GetString("file")
	if !mooring.ValidWireIdentifier(connection) || inputPath == "" {
		return errors.New("--connection and --file are required")
	}
	input := cmd.InOrStdin()
	if inputPath != "-" {
		file, err := os.Open(inputPath)
		if err != nil {
			return err
		}
		defer file.Close()
		input = file
	}
	// Closing the command-owned stream also releases a reader blocked while a
	// server revokes/closes the connection. CLI invocations own stdin lifetime.
	if closer, ok := input.(io.Closer); ok {
		defer closer.Close()
	}

	// NewRequest resolves the workspace and enforces the existing token-host
	// guard BEFORE the URL is converted to a WebSocket URL. No server-supplied
	// URL can redirect credentials or replace the configured API host.
	req, err := client.NewRequest(cmd.Context(), http.MethodGet,
		"/api/v1/pages/"+pagePathEscape(page)+"/mooring/ws?connection_ref="+url.QueryEscape(connection), nil)
	if err != nil {
		return err
	}
	endpoint := *req.URL
	origin := endpoint.Scheme + "://" + endpoint.Host
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	default:
		return errors.New("HTTP or HTTPS server required")
	}
	header := req.Header.Clone()
	header.Set("Origin", origin)
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 15 * time.Second}
	if client.HTTPClient != nil {
		if transport, ok := client.HTTPClient.Transport.(*http.Transport); ok {
			dialer.Proxy = transport.Proxy
			dialer.NetDialContext = transport.DialContext
			if transport.TLSClientConfig != nil {
				dialer.TLSClientConfig = transport.TLSClientConfig.Clone()
			}
		}
	}
	conn, response, err := dialer.DialContext(cmd.Context(), endpoint.String(), header)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return errors.New("Mooring connection rejected")
	}
	defer conn.Close()
	conn.SetReadLimit(mooringCLIFrameLimit)
	stop := context.AfterFunc(cmd.Context(), func() { _ = conn.Close() })
	defer stop()

	writeResult := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), mooringCLIFrameLimit+1)
		var sendErr error
		for scanner.Scan() {
			frame := scanner.Bytes()
			if len(frame) > mooringCLIFrameLimit || !json.Valid(frame) {
				sendErr = errors.New("bounded JSON frame required")
				break
			}
			if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				sendErr = err
				break
			}
			if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				sendErr = err
				break
			}
		}
		if sendErr == nil {
			sendErr = scanner.Err()
		}
		writeResult <- sendErr
		// EOF ends input, not delivery: wait for outstanding server replies.
		if sendErr != nil {
			_ = conn.Close()
		}
	}()
	for {
		kind, frame, err := conn.ReadMessage()
		if err != nil {
			select {
			case inputErr := <-writeResult:
				if inputErr != nil {
					return errors.New("Mooring input failed; retain original packets")
				}
			default:
			}
			if cmd.Context().Err() != nil {
				return cmd.Context().Err()
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return errors.New("Mooring connection ended; retain original packets")
		}
		if kind != websocket.TextMessage || !json.Valid(frame) {
			return errors.New("invalid Mooring server frame")
		}
		if err := formatter.WriteStreamRow(cli.RawJSON(frame)); err != nil {
			return err
		}
	}
}
