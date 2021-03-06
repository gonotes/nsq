package nsqd

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

const (
	MsgIDLength       = 16
	MsgTraceIDLength  = 8
	minValidMsgLength = MsgIDLength + 8 + 2 // Timestamp + Attempts
)

// the new message total id will be ID+TraceID, the length is same with old id
// slice, the traceid only used for trace for business, the ID is used for internal.
// In order to be compatible with old format, we keep the attempts field.
type FullMessageID [MsgIDLength]byte
type MessageID uint64

func GetMessageIDFromFullMsgID(id FullMessageID) MessageID {
	return MessageID(binary.BigEndian.Uint64(id[:8]))
}

func GetTraceIDFromFullMsgID(id FullMessageID) uint64 {
	return binary.BigEndian.Uint64(id[8 : 8+MsgTraceIDLength])
}

func PrintMessage(m *Message) string {
	return fmt.Sprintf("%v %v %s %v %v %v %v %v %v %v %v %v %v",
		m.ID, m.TraceID, string(m.Body), m.Timestamp, m.Attempts, m.deliveryTS,
		m.clientID, m.pri, m.index, m.deferredCnt, m.offset, m.rawMoveSize, m.queueCntIndex)
}

type Message struct {
	ID        MessageID
	TraceID   uint64
	Body      []byte
	Timestamp int64
	Attempts  uint16

	// for in-flight handling
	deliveryTS  time.Time
	clientID    int64
	pri         int64
	index       int
	deferredCnt int32
	//for backend queue
	offset        BackendOffset
	rawMoveSize   BackendOffset
	queueCntIndex int64
}

func MessageHeaderBytes() int {
	return MsgIDLength + 8 + 2
}

func NewMessage(id MessageID, body []byte) *Message {
	return &Message{
		ID:        id,
		TraceID:   0,
		Body:      body,
		Timestamp: time.Now().UnixNano(),
	}
}

func NewMessageWithTs(id MessageID, body []byte, ts int64) *Message {
	return &Message{
		ID:        id,
		TraceID:   0,
		Body:      body,
		Timestamp: ts,
	}
}

func (m *Message) GetFullMsgID() FullMessageID {
	var buf FullMessageID
	binary.BigEndian.PutUint64(buf[:8], uint64(m.ID))
	binary.BigEndian.PutUint64(buf[8:8+MsgTraceIDLength], uint64(m.TraceID))
	return buf
}

func (m *Message) IsDeferred() bool {
	return atomic.LoadInt32(&m.deferredCnt) > 0
}

func (m *Message) GetCopy() *Message {
	newMsg := *m
	newMsg.Body = make([]byte, len(m.Body))
	copy(newMsg.Body, m.Body)
	return &newMsg
}

func (m *Message) GetClientID() int64 {
	return m.clientID
}

func (m *Message) WriteToWithDetail(w io.Writer) (int64, error) {
	return m.internalWriteTo(w, true)
}

func (m *Message) WriteTo(w io.Writer) (int64, error) {
	return m.internalWriteTo(w, false)
}

func (m *Message) internalWriteTo(w io.Writer, writeDetail bool) (int64, error) {
	var buf [16]byte
	var total int64

	binary.BigEndian.PutUint64(buf[:8], uint64(m.Timestamp))
	binary.BigEndian.PutUint16(buf[8:10], m.Attempts)

	n, err := w.Write(buf[:10])
	total += int64(n)
	if err != nil {
		return total, err
	}

	binary.BigEndian.PutUint64(buf[:8], uint64(m.ID))
	binary.BigEndian.PutUint64(buf[8:8+MsgTraceIDLength], uint64(m.TraceID))
	n, err = w.Write(buf[:MsgIDLength])
	total += int64(n)
	if err != nil {
		return total, err
	}

	if writeDetail {
		binary.BigEndian.PutUint64(buf[:8], uint64(m.offset))
		binary.BigEndian.PutUint32(buf[8:8+4], uint32(m.rawMoveSize))
		n, err = w.Write(buf[:8+4])
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	n, err = w.Write(m.Body)
	total += int64(n)
	if err != nil {
		return total, err
	}

	return total, nil
}

func DecodeMessage(b []byte) (*Message, error) {
	return decodeMessage(b)
}

// note: the message body is using the origin buffer, so never modify the buffer after decode.
// decodeMessage deserializes data (as []byte) and creates a new Message
// message format:
// [x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x][x]...
// |       (int64)        ||    ||      (hex string encoded in ASCII)           || (binary)
// |       8-byte         ||    ||                 16-byte                      || N-byte
// ------------------------------------------------------------------------------------------...
//   nanosecond timestamp    ^^                   message ID                       message body
//                        (uint16)
//                         2-byte
//                        attempts
func decodeMessage(b []byte) (*Message, error) {
	var msg Message

	if len(b) < minValidMsgLength {
		return nil, fmt.Errorf("invalid message buffer size (%d)", len(b))
	}

	msg.Timestamp = int64(binary.BigEndian.Uint64(b[:8]))
	msg.Attempts = binary.BigEndian.Uint16(b[8:10])
	msg.ID = MessageID(binary.BigEndian.Uint64(b[10:18]))
	msg.TraceID = binary.BigEndian.Uint64(b[18:26])

	msg.Body = b[26:]
	return &msg, nil
}
