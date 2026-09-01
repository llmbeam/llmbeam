package remote

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

const (
	frameHeaders byte = 1
	frameBody    byte = 2
	frameEnd     byte = 3
	maxFrameSize      = 12 << 20
)

type requestFrame struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

type responseHeaders struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
}

func serveConnection(handler http.Handler, connection net.Conn) {
	defer connection.Close()
	for {
		payload, err := readFrame(connection)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		if err != nil {
			return
		}
		var frame requestFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			return
		}
		request, err := remoteRequest(frame)
		if err != nil {
			return
		}
		writer := &frameResponseWriter{connection: connection, header: make(http.Header)}
		handler.ServeHTTP(writer, request)
		if err := writer.finish(); err != nil {
			return
		}
	}
}

func remoteRequest(frame requestFrame) (*http.Request, error) {
	if frame.Method == "" || frame.Path == "" || frame.Path[0] != '/' {
		return nil, errors.New("invalid remote request")
	}
	request, err := http.NewRequest(frame.Method, "http://llmbeam.remote"+frame.Path, bytes.NewBufferString(frame.Body))
	if err != nil {
		return nil, err
	}
	request.Host = "llmbeam.remote"
	request.RemoteAddr = "tailcat:1"
	for name, values := range frame.Headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return request, nil
}

type frameResponseWriter struct {
	connection  io.Writer
	header      http.Header
	wroteHeader bool
	status      int
	err         error
}

func (writer *frameResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *frameResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.status = status
	payload, err := json.Marshal(responseHeaders{Status: status, Headers: writer.header})
	if err != nil {
		writer.err = err
		return
	}
	writer.err = writeTypedFrame(writer.connection, frameHeaders, payload)
}

func (writer *frameResponseWriter) Write(payload []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.err != nil {
		return 0, writer.err
	}
	if err := writeTypedFrame(writer.connection, frameBody, payload); err != nil {
		writer.err = err
		return 0, err
	}
	return len(payload), nil
}

func (writer *frameResponseWriter) Flush() {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
}

func (writer *frameResponseWriter) finish() error {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.err != nil {
		return writer.err
	}
	return writeTypedFrame(writer.connection, frameEnd, nil)
}

func readFrame(reader io.Reader) ([]byte, error) {
	var sizeBuffer [4]byte
	if _, err := io.ReadFull(reader, sizeBuffer[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(sizeBuffer[:])
	if size == 0 || size > maxFrameSize {
		return nil, fmt.Errorf("invalid frame size %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeTypedFrame(writer io.Writer, frameType byte, payload []byte) error {
	size := len(payload) + 1
	if size > maxFrameSize {
		return fmt.Errorf("frame too large: %d", size)
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[:4], uint32(size))
	header[4] = frameType
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
