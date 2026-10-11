package vault

import (
	"context"
	"fmt"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
	"github.com/danielpina1/nexus/backend/internal/vault/index"
	"github.com/danielpina1/nexus/backend/internal/vault/note"
	"github.com/danielpina1/nexus/backend/kernel"
)

// AskModel is the model alias used to answer questions from the vault.
const AskModel = "smart"

const askSystem = `You answer questions using only the user's notes below.
Cite every note you use inline as [[Note title]]. If the notes do not contain
the answer, say so briefly. Answer in the language of the question.`

// Ask answers a question from the vault. With an LLM available it sends the
// top hits (title, path, excerpt) to the "smart" model and asks for an answer
// with [[citations]]; otherwise (or if the model call fails) the answer is
// the list of matching snippets. Sources are the notes retrieved.
func (v *Vault) Ask(ctx context.Context, question string) (string, []*nexusv1.NoteRef, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", nil, fmt.Errorf("%w: empty question", ErrInvalid)
	}
	hits, err := v.ix.Search(ctx, index.Query{Text: question, Limit: 6})
	if err != nil {
		return "", nil, err
	}
	var sources []*nexusv1.NoteRef
	for _, h := range hits {
		sources = append(sources, entryRef(h.Entry))
	}
	if len(hits) == 0 {
		return "I couldn't find anything about that in your notes.", nil, nil
	}
	if v.LLM != nil {
		if llm, ok := v.LLM(); ok && llm != nil {
			answer, err := v.askLLM(ctx, llm, question, hits)
			if err == nil && strings.TrimSpace(answer) != "" {
				return answer, sources, nil
			}
			v.log.Warn("vault: ask via LLM failed; returning snippets", "err", err)
		}
	}
	var b strings.Builder
	b.WriteString("Relevant notes:\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "\n- [[%s]]: %s", h.Entry.Title, h.Snippet)
	}
	return b.String(), sources, nil
}

func (v *Vault) askLLM(ctx context.Context, llm kernel.LLM, question string, hits []index.Hit) (string, error) {
	var ctxText strings.Builder
	for i, h := range hits {
		excerpt := h.Snippet
		if b, _, err := v.ReadRaw(ctx, h.Entry.Path); err == nil {
			excerpt = truncate(strings.TrimSpace(note.Parse(h.Entry.Path, b).Body), 2000)
		}
		fmt.Fprintf(&ctxText, "### %d. [[%s]] (%s)\n%s\n\n", i+1, h.Entry.Title, h.Entry.Path, excerpt)
	}
	resp, err := llm.Chat(ctx, kernel.ChatRequest{
		Model: AskModel,
		Messages: []kernel.ChatMessage{
			{Role: "system", Content: askSystem},
			{Role: "user", Content: "Notes:\n\n" + ctxText.String() + "Question: " + question},
		},
		MaxTokens: 800,
	})
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}
