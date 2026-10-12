package security

import (
	"context"
	"crypto"
)

// HubSigner returns the hub identity signing key. Other hub modules use it to
// sign data on behalf of the hub (e.g. notify seals notification envelopes
// with it so phones can verify the sender). It never leaves the process.
func (s *Security) HubSigner() crypto.Signer { return s.hubKey }

// DropPushToken clears a device's APNs token after APNs reported it as
// unregistered (HTTP 410). It only clears the token if it still matches, so a
// token re-registered in the meantime is kept.
func (s *Security) DropPushToken(ctx context.Context, deviceID, token string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE security_devices SET push_token = '' WHERE id = ? AND push_token = ?`, deviceID, token)
	return err
}

// DropLiveActivityToken deletes a Live Activity token APNs reported as
// unregistered.
func (s *Security) DropLiveActivityToken(ctx context.Context, deviceID string, token []byte) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM security_live_activity_tokens WHERE device_id = ? AND token = ?`, deviceID, token)
	return err
}
