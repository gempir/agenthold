package enrollment

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const (
	protocolVersion = 1
	maxFrame        = 64 << 10
)

// hello is the first message a node sends after the TLS handshake.
type hello struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"` // "join" or "connect"
	Secret  string `json:"secret,omitempty"`
	Name    string `json:"name,omitempty"`
	// NodeVersion is the agenthold version of the node. Only nodes that send it are told where
	// the controller serves downloads from: older nodes reject replies with fields they do not know.
	NodeVersion string `json:"nodeVersion,omitempty"`
}

type welcome struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	NodeID  string `json:"nodeId,omitempty"`
	Name    string `json:"name,omitempty"`
	// ControllerVersion and DownloadURL are the controller's agenthold version and the /dl/ root it
	// serves its release (including the base image) from, for nodes that sent NodeVersion.
	ControllerVersion string `json:"controllerVersion,omitempty"`
	DownloadURL       string `json:"downloadUrl,omitempty"`

	// legacy is set locally when the controller only answered a hello without NodeVersion.
	legacy bool
}

// readFrame reads exactly one length-prefixed JSON object. A buffered decoder
// could read ahead into the multiplexed stream that follows the handshake.
func readFrame(r io.Reader, out any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxFrame {
		return errors.New("invalid handshake frame size")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid handshake message")
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxFrame {
		return errors.New("invalid handshake message")
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	_, err = w.Write(frame)
	return err
}
