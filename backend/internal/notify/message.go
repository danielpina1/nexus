package notify

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielpina1/nexus/backend/kernel"
)

// Notification categories (IOS_LIVE.md §3.1; ios/App/System/Notifications.swift).
const (
	CatApproval          = "APPROVAL"
	CatApprovalBiometric = "APPROVAL_BIOMETRIC"
	CatQuestion          = "QUESTION"
	CatResult            = "RESULT"
	CatAlert             = "ALERT"
	CatBrief             = "BRIEF"
	CatFactReview        = "FACT_REVIEW"
	CatSuggestion        = "SUGGESTION"
)

// Interruption levels (aps.interruption-level).
const (
	LevelPassive       = "passive"
	LevelActive        = "active"
	LevelTimeSensitive = "time-sensitive"
)

// Generic visible alert when content is encrypted.
const (
	GenericTitle = "Nexus"
	GenericBody  = "New activity"
)

// Data keys understood in kernel.Notification.Data.
const (
	DataTaskID        = "task_id"
	DataApprovalID    = "approval_id"
	DataQuestionID    = "question_id"
	DataAgentID       = "agent_id"
	DataAgentName     = "agent_name"
	DataSubtitle      = "subtitle"
	DataAttachmentURL = "attachment_url"
	DataChoices       = "choices"      // JSON array of strings, or "a|b|c"
	DataItems         = "items"        // JSON array of {name, detail, amount}
	DataInterruption  = "interruption" // passive | active | time-sensitive
)

// Item is a line in a rich notification (cart line, diff file…).
type Item struct {
	Name   string  `json:"name"`
	Detail *string `json:"detail,omitempty"`
	Amount *string `json:"amount,omitempty"`
}

// Message is a fully described notification for one or more devices.
type Message struct {
	DeviceIDs     []string // empty = all devices
	Category      string
	Title         string
	Subtitle      string
	Body          string
	ThreadID      string
	CollapseID    string
	TaskID        string
	ApprovalID    string
	QuestionID    string
	AgentID       string
	AgentName     string
	AttachmentURL string
	Choices       []string
	Items         []Item
	Interruption  string
	// Expiration is apns-expiration (zero = 24 h).
	Expiration time.Time
}

// Plaintext is the JSON sealed inside `nexus_envelope`. It mirrors
// NotificationPlaintext in ios/NexusKit/Sources/NexusCore/Security/EnvelopeCrypto.swift
// (snake_case keys; title and body are required).
type Plaintext struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle,omitempty"`
	Body          string   `json:"body"`
	Category      string   `json:"category,omitempty"`
	ThreadID      string   `json:"thread_id,omitempty"`
	TaskID        string   `json:"task_id,omitempty"`
	ApprovalID    string   `json:"approval_id,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	AttachmentURL string   `json:"attachment_url,omitempty"`
	Choices       []string `json:"choices,omitempty"`
	Items         []Item   `json:"items,omitempty"`
}

func (m *Message) plaintext() Plaintext {
	return Plaintext{
		Title: m.Title, Subtitle: m.Subtitle, Body: m.Body, Category: m.Category,
		ThreadID: m.ThreadID, TaskID: m.TaskID, ApprovalID: m.ApprovalID,
		AgentID: m.AgentID, AgentName: m.AgentName, AttachmentURL: m.AttachmentURL,
		Choices: m.Choices, Items: m.Items,
	}
}

// defaultLevel is the interruption level per category (IOS_LIVE.md §3.1).
func defaultLevel(cat string) string {
	switch cat {
	case CatApproval, CatApprovalBiometric, CatAlert:
		return LevelTimeSensitive
	case CatBrief, CatFactReview, CatSuggestion:
		return LevelPassive
	default:
		return LevelActive
	}
}

func (m *Message) level() string {
	if m.Interruption != "" {
		return m.Interruption
	}
	return defaultLevel(m.Category)
}

// FromNotification converts the kernel's channel-agnostic notification.
func FromNotification(n kernel.Notification) Message {
	d := n.Data
	m := Message{
		DeviceIDs:     n.DeviceIDs,
		Category:      n.Category,
		Title:         n.Title,
		Body:          n.Body,
		ThreadID:      n.ThreadID,
		CollapseID:    n.CollapseID,
		TaskID:        d[DataTaskID],
		ApprovalID:    d[DataApprovalID],
		QuestionID:    d[DataQuestionID],
		AgentID:       d[DataAgentID],
		AgentName:     d[DataAgentName],
		Subtitle:      d[DataSubtitle],
		AttachmentURL: d[DataAttachmentURL],
		Interruption:  d[DataInterruption],
	}
	if m.TaskID == "" {
		m.TaskID = n.ThreadID
	}
	if n.TimeSensitive {
		m.Interruption = LevelTimeSensitive
	}
	if c := d[DataChoices]; c != "" {
		if json.Unmarshal([]byte(c), &m.Choices) != nil {
			m.Choices = strings.Split(c, "|")
		}
	}
	if it := d[DataItems]; it != "" {
		_ = json.Unmarshal([]byte(it), &m.Items)
	}
	return m
}

// truncate cuts s to at most n bytes on a rune boundary, adding "…".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return ""
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
