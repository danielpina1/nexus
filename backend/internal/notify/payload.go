package notify

import (
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/security/envelope"
	"github.com/danielpina1/nexus/backend/internal/security/keys"
)

// MaxPayload is the APNs payload limit for alerts and Live Activities.
const MaxPayload = 4096

// EnvelopeKey is the top-level APNs payload key holding the sealed content
// (EnvelopeFormat.pushPayloadKey on iOS).
const EnvelopeKey = "nexus_envelope"

type apsAlert struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body,omitempty"`
}

type alertAPS struct {
	Alert             *apsAlert `json:"alert,omitempty"`
	MutableContent    int       `json:"mutable-content,omitempty"`
	Category          string    `json:"category,omitempty"`
	ThreadID          string    `json:"thread-id,omitempty"`
	Sound             string    `json:"sound,omitempty"`
	InterruptionLevel string    `json:"interruption-level,omitempty"`
	RelevanceScore    *float64  `json:"relevance-score,omitempty"`
}

// alertPayload is the APNs JSON of a notification. Ids (never content) are
// repeated at top level so the app's action handler works even when the
// service extension could not decrypt (NotificationResponseHandler reads
// task_id / approval_id / question_id from userInfo).
type alertPayload struct {
	APS        alertAPS `json:"aps"`
	Envelope   string   `json:"nexus_envelope,omitempty"`
	TaskID     string   `json:"task_id,omitempty"`
	ApprovalID string   `json:"approval_id,omitempty"`
	QuestionID string   `json:"question_id,omitempty"`
}

// sealer seals notification plaintexts for devices.
type sealer struct {
	signer   crypto.Signer
	senderID string

	mu      sync.Mutex
	counter uint64
	now     func() time.Time
}

func newSealer(signer crypto.Signer, now func() time.Time) (*sealer, error) {
	pk, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("notify: envelope signer must be ECDSA P-256")
	}
	if now == nil {
		now = time.Now
	}
	return &sealer{signer: signer, senderID: keys.KeyID(pk), now: now}, nil
}

// nextCounter is strictly increasing, also across restarts (seeded from the
// clock in microseconds).
func (s *sealer) nextCounter() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := uint64(s.now().UnixMicro())
	if c <= s.counter {
		c = s.counter + 1
	}
	s.counter = c
	return c
}

// Seal returns base64 (std, padded) of the protobuf SealedEnvelope
// (purpose NOTIFICATION) addressed to the device's X25519 key.
func (s *sealer) Seal(plaintext, recipientPub []byte, correlationID string) (string, error) {
	hdr, err := envelope.NewHeader(s.senderID, keys.KeyIDBytes(recipientPub),
		nexusv1.EnvelopePurpose_ENVELOPE_PURPOSE_NOTIFICATION, s.nextCounter(), correlationID)
	if err != nil {
		return "", err
	}
	env, err := envelope.Seal(hdr, plaintext, recipientPub, s.signer)
	if err != nil {
		return "", err
	}
	b, err := proto.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func relevance(level string) *float64 {
	v := 0.5
	switch level {
	case LevelTimeSensitive:
		v = 1
	case LevelPassive:
		v = 0.1
	}
	return &v
}

// buildAlert renders the APNs JSON for m and one device. With sl != nil and
// encPub set, the content is sealed and the visible alert is generic;
// encrypt without a key yields the generic alert only (never plaintext).
// Strings are shortened until the payload fits MaxPayload.
func buildAlert(m Message, level string, encrypt bool, sl *sealer, encPub []byte) ([]byte, error) {
	budget := 2048
	for range 8 {
		mm := m
		mm.Body = truncate(m.Body, budget)
		mm.Title = truncate(m.Title, 256)
		mm.Subtitle = truncate(m.Subtitle, 256)
		p := alertPayload{
			APS: alertAPS{
				Category:          mm.Category,
				ThreadID:          mm.ThreadID,
				InterruptionLevel: level,
				RelevanceScore:    relevance(level),
			},
			TaskID: mm.TaskID, ApprovalID: mm.ApprovalID, QuestionID: mm.QuestionID,
		}
		if level != LevelPassive {
			p.APS.Sound = "default"
		}
		if encrypt {
			p.APS.Alert = &apsAlert{Title: GenericTitle, Body: GenericBody}
			p.APS.MutableContent = 1
			if sl != nil && len(encPub) == 32 {
				pt, err := json.Marshal(mm.plaintext())
				if err != nil {
					return nil, err
				}
				corr := mm.CollapseID
				if corr == "" {
					corr = mm.TaskID
				}
				env, err := sl.Seal(pt, encPub, corr)
				if err != nil {
					return nil, err
				}
				p.Envelope = env
			}
		} else {
			p.APS.Alert = &apsAlert{Title: mm.Title, Subtitle: mm.Subtitle, Body: mm.Body}
		}
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxPayload {
			return b, nil
		}
		budget /= 2
		if len(m.Items) > 0 {
			m.Items = m.Items[:len(m.Items)/2]
		}
		if len(m.Choices) > 4 {
			m.Choices = m.Choices[:4]
		}
	}
	return nil, errors.New("notify: payload exceeds 4 KB")
}
