// Package mail sends outbound EDI mail (file or Postmark) and parses Postmark inbound webhooks.
package mail

import "context"

type Attachment struct {
	Name, ContentType string
	Data              []byte
}

type Outgoing struct {
	To            []string
	Subject, Text string
	Attachments   []Attachment
}

type Mailer interface {
	Send(ctx context.Context, m Outgoing) error
	Name() string // "file" or "postmark"
}

type Inbound struct {
	To                  []string
	From, Subject, Text string
	Trusted             bool
	Attachments         []Attachment
}
