package protocol

import "encoding/json"

// Encode wraps a typed payload into an Envelope for transmission.
func Encode(msgType MessageType, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		Type:    msgType,
		Payload: p,
	})
}

// Decode parses a raw WebSocket message into an Envelope.
func Decode(data []byte) (Envelope, error) {
	var env Envelope
	err := json.Unmarshal(data, &env)
	return env, err
}

// DecodePayload unmarshals the payload of an Envelope into the given target.
func DecodePayload(env Envelope, target any) error {
	return json.Unmarshal(env.Payload, target)
}
