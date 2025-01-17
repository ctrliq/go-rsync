package rsync

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

	"golang.org/x/crypto/md4" //nolint // md4 is used for rsync protocol
)

type ClientOption func(*clientOptions)

func WithClientAuth(username, password string) ClientOption {
	return func(c *clientOptions) {
		c.Auth = &ClientAuth{
			Username: username,
			Password: password,
		}
	}
}

func WithExclusionList(exclusionList ExclusionList) ClientOption {
	return func(c *clientOptions) {
		c.ExclusionList = exclusionList
	}
}

func WithLogger(logger *slog.Logger) ClientOption {
	return func(c *clientOptions) {
		c.Logger = logger
	}
}

type ClientAuth struct {
	Username string
	Password string
}

type clientOptions struct {
	Auth          *ClientAuth
	ExclusionList ExclusionList
	Logger        *slog.Logger
}

/* As a Client, we need to:
1. connect to server by socket or ssh
2. handshake: version, args, ioerror
	PS: client always sends exclusions/filter list
3. construct a Receiver or a Sender, then excute it.
*/

// TODO: passes more arguments: cmd
// Connect to rsync daemon
func SocketClient(storage FS, address string, module string, path string, opts ...ClientOption) (SendReceiver, error) {
	co := clientOptions{
		Auth:          nil,
		ExclusionList: make(ExclusionList, 0),
		Logger:        slog.Default(),
	}

	for _, opt := range opts {
		opt(&co)
	}

	skt, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}

	conn := &Conn{
		Writer:    skt,
		Reader:    skt,
		Bytespool: make([]byte, 8),
	}

	/* HandShake by socket */
	// send my version
	_, err = conn.Write([]byte(RSYNC_VERSION))
	if err != nil {
		return nil, err
	}

	// receive server's protocol version and seed
	versionStr, _ := readLine(conn)

	// recv(version)
	var remoteProtocol, remoteProtocolSub int
	_, err = fmt.Sscanf(versionStr, "@RSYNCD: %d.%d\n", &remoteProtocol, &remoteProtocolSub)
	if err != nil {
		co.Logger.Error("failed to parse version", slog.String("error", err.Error()))
	}
	co.Logger.Debug("remote protocol", slog.Int("protocol", remoteProtocol), slog.Int("sub", remoteProtocolSub))

	buf := new(bytes.Buffer)

	// send mod name
	buf.WriteString(module)
	buf.WriteByte('\n')
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}
	buf.Reset()

	// Wait for '@RSYNCD: OK'
	for {
		res, err := readLine(conn)
		if err != nil {
			return nil, err
		}

		if strings.Contains(res, RSYNCD_OK) {
			co.Logger.Debug("OK received")
			break
		}

		// Check for auth request
		if strings.Contains(res, RSYNC_AUTHREQD) {
			if co.Auth == nil {
				return nil, fmt.Errorf("server requires auth")
			}

			// Parse challenge from server
			var challenge string
			_, err = fmt.Sscanf(res, "@RSYNCD: AUTHREQD %s", &challenge)
			if err != nil {
				return nil, fmt.Errorf("failed to parse challenge")
			}

			// Calculate challenge response with md4 of password + challenge
			h := md4.New()
			h.Write([]byte("\x00\x00\x00\x00"))
			_, err := io.WriteString(h, co.Auth.Password)
			if err != nil {
				return nil, err
			}

			_, err = io.WriteString(h, challenge)
			if err != nil {
				return nil, err
			}

			buf.WriteString(fmt.Sprintf("%s %s\n", co.Auth.Username, base64.RawStdEncoding.EncodeToString(h.Sum(nil))))

			_, err = conn.Write(buf.Bytes())
			if err != nil {
				return nil, err
			}
			buf.Reset()
		}
	}

	// Send arguments
	buf.WriteString(SAMPLE_ARGS)
	buf.WriteString(module)
	buf.WriteString(path)
	buf.WriteString("\n\n")
	_, err = conn.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}
	buf.Reset()

	// read int32 as seed
	seed, err := conn.ReadInt()
	if err != nil {
		return nil, err
	}
	co.Logger.Debug("seed received", slog.Any("seed", seed))

	// HandShake OK
	co.Logger.Debug("handshake completed")

	// Begin to demux
	conn.Reader = NewMuxReader(conn.Reader, co.Logger)

	// Send exclusion list
	err = co.ExclusionList.SendExlusionList(conn)
	if err != nil {
		return nil, err
	}

	// TODO: Sender

	return &Receiver{
		Conn:    conn,
		Module:  module,
		Path:    path,
		Seed:    seed,
		Storage: storage,
		Logger:  co.Logger,
	}, nil
}

// Connect to sshd, and start a rsync server on remote
func SSHClient(storage FS, address string, module string, path string, _ map[string]string) (SendReceiver, error) {
	// TODO: build args

	ssh, err := NewSSH(address, "", "", "rsync --server --sender -l -p -r -t")
	if err != nil {
		return nil, err
	}
	conn := &Conn{
		Writer:    ssh,
		Reader:    ssh,
		Bytespool: make([]byte, 8),
	}

	// Handshake
	lver, err := conn.ReadInt()
	if err != nil {
		return nil, err
	}

	rver, err := conn.ReadInt()
	if err != nil {
		return nil, err
	}

	seed, err := conn.ReadInt()
	if err != nil {
		return nil, err
	}

	// TODO: Sender

	return &Receiver{
		Conn:    conn,
		Module:  module,
		Path:    path,
		Seed:    seed,
		LVer:    lver,
		RVer:    rver,
		Storage: storage,
	}, nil
}
