package application

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
	"github.com/mlnomadpy/dacli/internal/operatorread"
)

// ServeOperatorTransport owns one bounded JSON-lines stream. The caller is the
// native app's child process; EOF ends it and no listener is opened.
func ServeOperatorTransport(ctx context.Context, input io.Reader, output io.Writer, server operatorread.Server) error {
	reader := bufio.NewReaderSize(input, operatorread.MaxRequestBytes+2)
	writer := bufio.NewWriter(output)
	for {
		readResult := make(chan operatorFrameResult, 1)
		go func() {
			frame, err := readOperatorFrame(reader)
			readResult <- operatorFrameResult{frame: frame, err: err}
		}()
		var frame []byte
		var err error
		select {
		case result := <-readResult:
			frame, err = result.frame, result.err
		case <-ctx.Done():
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) && len(frame) == 0 {
			return nil
		}
		if err != nil && !errors.Is(err, errOperatorFrameTooLarge) {
			if len(frame) == 0 {
				return err
			}
		}
		if errors.Is(err, errOperatorFrameTooLarge) {
			if writeErr := writeOperatorFrameBounded(ctx, writer, operatorread.FailureFrame("", operatorv1.CodeLimitExceeded, "request exceeds the bounded operator frame"), operatorread.DefaultDeadline); writeErr != nil {
				return writeErr
			}
			return nil
		}
		deadline := server.Deadline
		if deadline <= 0 {
			deadline = operatorread.DefaultDeadline
		}
		result := make(chan []byte, 1)
		go func(request []byte) { result <- server.Handle(ctx, request) }(append([]byte(nil), frame...))
		timer := time.NewTimer(deadline)
		var response []byte
		select {
		case response = <-result:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if writeErr := writeOperatorFrameBounded(ctx, writer, operatorread.FailureFrame(requestIDFromFrame(frame), operatorv1.CodeInternal, "operator read deadline exceeded"), operatorread.DefaultDeadline); writeErr != nil {
				return writeErr
			}
			return errors.New("operator read deadline exceeded; owned process must exit")
		}
		if writeErr := writeOperatorFrameBounded(ctx, writer, response, deadline); writeErr != nil {
			return writeErr
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

type operatorFrameResult struct {
	frame []byte
	err   error
}

var errOperatorFrameTooLarge = errors.New("operator frame too large")

func readOperatorFrame(reader *bufio.Reader) ([]byte, error) {
	frame := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > operatorread.MaxRequestBytes+1 {
			return nil, errOperatorFrameTooLarge
		}
		frame = append(frame, part...)
		if err == nil {
			frame = frame[:len(frame)-1]
			return frame, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(frame) > operatorread.MaxRequestBytes {
				return nil, errOperatorFrameTooLarge
			}
			continue
		}
		if errors.Is(err, io.EOF) && len(frame) > 0 {
			return frame, io.EOF
		}
		return frame, err
	}
}

func writeOperatorFrame(writer *bufio.Writer, raw []byte) error {
	if len(raw) > operatorread.MaxResponseBytes {
		return errors.New("operator response exceeds frame limit")
	}
	if _, err := writer.Write(raw); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}

func writeOperatorFrameBounded(ctx context.Context, writer *bufio.Writer, raw []byte, deadline time.Duration) error {
	result := make(chan error, 1)
	go func() { result <- writeOperatorFrame(writer, raw) }()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("operator response write deadline exceeded; owned process must exit")
	}
}

func requestIDFromFrame(frame []byte) string {
	var request struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(frame, &request)
	if len(request.RequestID) > 128 {
		return ""
	}
	return request.RequestID
}
