package remote

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestCompressToken(t *testing.T) {
	raw := []byte(strings.Repeat("region-host derp.example.com ", 20))
	original := "tc" + base64.RawURLEncoding.EncodeToString(raw)
	compressed := compressToken(original)
	if compressed == original {
		t.Fatal("token was not compressed")
	}
	if compressed[:2] != "tg" {
		t.Fatalf("compressed token prefix = %q", compressed[:2])
	}
	encoded, err := base64.RawURLEncoding.DecodeString(compressed[2:])
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded token payload = %q", decoded)
	}
}

func TestServeConnectionBridgesStreamingHTTP(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	t.Cleanup(func() { clientConnection.Close() })
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/test" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Cookie") != "session=token" {
			t.Errorf("Cookie = %q", request.Header.Get("Cookie"))
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"hello":"world"}` {
			t.Errorf("body = %q", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("data: one\n\n"))
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte("data: two\n\n"))
	})
	go serveConnection(handler, serverConnection)

	requestPayload, _ := json.Marshal(requestFrame{
		Method:  http.MethodPost,
		Path:    "/api/test",
		Headers: map[string][]string{"Cookie": {"session=token"}},
		Body:    `{"hello":"world"}`,
	})
	writeRequestFrame(t, clientConnection, requestPayload)

	frameType, payload := readTypedFrame(t, clientConnection)
	if frameType != frameHeaders {
		t.Fatalf("first frame type = %d", frameType)
	}
	var headers responseHeaders
	if err := json.Unmarshal(payload, &headers); err != nil {
		t.Fatal(err)
	}
	if headers.Status != http.StatusAccepted || headers.Headers["Content-Type"][0] != "text/event-stream" {
		t.Fatalf("headers = %+v", headers)
	}

	var body strings.Builder
	for {
		frameType, payload = readTypedFrame(t, clientConnection)
		if frameType == frameEnd {
			break
		}
		if frameType != frameBody {
			t.Fatalf("body frame type = %d", frameType)
		}
		body.Write(payload)
	}
	if body.String() != "data: one\n\ndata: two\n\n" {
		t.Fatalf("response body = %q", body.String())
	}
}

func writeRequestFrame(t *testing.T, writer io.Writer, payload []byte) {
	t.Helper()
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	if _, err := writer.Write(append(size[:], payload...)); err != nil {
		t.Fatal(err)
	}
}

func readTypedFrame(t *testing.T, reader io.Reader) (byte, []byte) {
	t.Helper()
	payload, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Fatal("empty typed frame")
	}
	return payload[0], payload[1:]
}
