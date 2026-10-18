package learn

import (
	"fmt"
	"math/rand/v2"
	"strings"

	nexusv1 "github.com/danielpina1/nexus/backend/gen/nexus/v1"
)

// Synthetic routing data: six agents whose users phrase requests with
// vocabulary that mostly does NOT appear in the agent descriptions, so the
// description stage alone is weak and the learned stages must pick up the
// owner's habits from labelled history.

type synthAgent struct {
	id, name, desc string
	examples       []string
	templates      []string
	slots          [][]string
}

var synthAgents = []synthAgent{
	{id: "grocery", name: "Grocery (Continente)", desc: "Buys groceries on continente.pt from a shopping list.",
		examples:  []string{"buy the usual groceries"},
		templates: []string{"we are out of %s and %s", "order %s, %s and more for the week", "restock %s please, also %s", "get %s and %s delivered"},
		slots: [][]string{{"milk", "eggs", "bread", "coffee", "rice", "bananas", "yogurt", "olive oil", "pasta", "tomatoes", "cheese", "butter"},
			{"apples", "detergent", "toilet paper", "chicken", "salmon", "oats", "sugar", "flour", "water bottles", "onions"}}},
	{id: "coder", name: "Code fixer", desc: "Fixes failing tests and bugs in repositories.",
		examples:  []string{"fix the failing tests"},
		templates: []string{"the %s is red on %s", "%s panic in %s", "%s after the merge into %s", "investigate the %s in %s"},
		slots: [][]string{{"CI pipeline", "build", "nil pointer", "lint check", "race detector", "integration suite", "type error", "segfault"},
			{"main", "handler.go", "the api service", "nexus backend", "the web app", "release branch", "router module"}}},
	{id: "travel", name: "Travel planner", desc: "Plans trips, flights and hotels.",
		examples:  []string{"plan a trip"},
		templates: []string{"weekend in %s %s", "cheapest way to get to %s %s", "somewhere to stay in %s %s", "itinerary for %s %s"},
		slots: [][]string{{"Porto", "Madrid", "Rome", "Paris", "Berlin", "London", "Seville", "Prague", "Vienna", "Lyon"},
			{"next month", "in June", "for the holidays", "this autumn", "with the kids", "for two nights", "around Easter"}}},
	{id: "researcher", name: "Researcher", desc: "Researches one question on the web with citations.",
		examples:  []string{"research this question"},
		templates: []string{"what is the evidence on %s %s", "state of the art in %s %s", "is %s really %s", "summarise studies about %s %s"},
		slots: [][]string{{"intermittent fasting", "solid state batteries", "creatine", "remote work", "heat pumps", "microplastics", "nuclear fusion", "sleep tracking"},
			{"for health", "in 2026", "worth it", "overhyped", "according to papers", "for productivity", "long term"}}},
	{id: "receipts", name: "Receipt scanner", desc: "Extracts expenses from receipt photos.",
		examples:  []string{"scan this receipt"},
		templates: []string{"log this %s %s", "add the %s %s to expenses", "file the %s %s", "expense the %s %s"},
		slots: [][]string{{"lunch", "taxi", "hotel", "dinner", "parking", "train", "conference", "office supplies"},
			{"bill", "invoice", "ticket", "fatura", "charge", "payment slip"}}},
	{id: "ops", name: "Ops", desc: "Monitors servers and restarts services.",
		examples:  []string{"restart the service"},
		templates: []string{"%s is %s on the vps", "%s %s again", "check why %s is %s", "%s looks %s"},
		slots: [][]string{{"nginx", "postgres", "the disk", "redis", "the docker daemon", "home assistant", "the backup job", "grafana"},
			{"down", "full", "slow", "crashing", "unreachable", "stuck", "out of memory"}}},
}

func synthCandidates() []Candidate {
	var out []Candidate
	for _, a := range synthAgents {
		out = append(out, Candidate{ID: a.id, Kind: KindAgent, Name: a.name, Description: a.desc, Examples: a.examples,
			Tier: nexusv1.Tier_TIER_MEDIUM, Runtimes: []string{"claude-code", "codex", "ollama"}})
	}
	return out
}

type synthReq struct{ input, label string }

func synthRequests(n int, seed uint64) []synthReq {
	r := rand.New(rand.NewPCG(seed, 11))
	out := make([]synthReq, 0, n)
	for i := 0; i < n; i++ {
		a := synthAgents[r.IntN(len(synthAgents))]
		t := a.templates[r.IntN(len(a.templates))]
		args := make([]any, len(a.slots))
		for j, s := range a.slots {
			args[j] = s[r.IntN(len(s))]
		}
		in := fmt.Sprintf(t, args...)
		// Ambiguity: generic phrasing and words borrowed from another agent.
		if r.Float64() < 0.25 {
			o := synthAgents[r.IntN(len(synthAgents))]
			in += " " + o.slots[0][r.IntN(len(o.slots[0]))]
		}
		if r.Float64() < 0.1 {
			in = "can you sort out " + args[0].(string)
		}
		label := a.id
		if r.Float64() < 0.06 { // the owner is not always consistent
			label = synthAgents[r.IntN(len(synthAgents))].id
		}
		out = append(out, synthReq{input: strings.TrimSpace(in), label: label})
	}
	return out
}

// synthExamples turns requests into confirmed examples with decision ids.
func synthExamples(reqs []synthReq, prefix string) []Example {
	out := make([]Example, len(reqs))
	for i, q := range reqs {
		out[i] = Example{DecisionID: fmt.Sprintf("%s%04d", prefix, i), Input: q.input, Label: q.label, Weight: wSuccess}
	}
	return out
}
