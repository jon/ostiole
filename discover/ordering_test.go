package discover

import (
	"context"
	"errors"
	"iter"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/jon/ostiole/probe"
)

func TestProbeOrderingAndDetachedAmbiguity(t *testing.T) {
	input := []Candidate{
		NewCandidate(probe.Info{}, "0", noOpen),
		NewCandidate(probe.Info{Serial: "A"}, "1", noOpen),
		NewCandidate(probe.Info{Serial: "a"}, "2", noOpen),
		NewCandidate(probe.Info{Serial: "a", Function: "A"}, "3", noOpen),
		NewCandidate(probe.Info{Serial: "a", Function: "B", Location: "1"}, "4", noOpen),
		NewCandidate(probe.Info{Serial: "a", Function: "B", Location: "2", Product: "a"}, "5", noOpen),
		NewCandidate(probe.Info{Serial: "a", Function: "B", Location: "2", Product: "b"}, "6", noOpen),
		NewCandidate(probe.Info{Serial: "a", Function: "B", Location: "2", Product: "b"}, "7", noOpen),
	}
	var r Registry
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) {
		return []Attachment{testAttachment{key: "one"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterProbe(NewProbeProvider("p", "usb", func(context.Context, Transport) ([]Candidate, error) { return input, nil })); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 16 {
		rng.Shuffle(len(input), func(i, j int) { input[i], input[j] = input[j], input[i] })
		i, err := r.Probes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got := slices.Collect(iter.Seq[Candidate](i))
		for n, c := range got {
			if c.key != string(rune('0'+n)) {
				t.Fatalf("order at %d: %s", n, c.key)
			}
		}
		_, err = i.Select(Selection{})
		var ambiguous *SelectionError
		if !errors.As(err, &ambiguous) {
			t.Fatal(err)
		}
		metadata := ambiguous.Candidates()
		metadata[0].Serial = "mutated"
		if ambiguous.Candidates()[0].Serial != "" {
			t.Fatal("ambiguity aliases metadata")
		}
		for n, info := range ambiguous.Candidates() {
			if info != got[n].Info() {
				t.Fatal("ambiguity order changed")
			}
		}
		input[0].info.Serial = "changed after discovery"
		if slices.Collect(iter.Seq[Candidate](i))[0].Info() != got[0].Info() {
			t.Fatal("snapshot changed")
		}
		input[0].info.Serial = got[slices.IndexFunc(got, func(c Candidate) bool { return c.key == input[0].key })].Info().Serial
	}
}

func noOpen(context.Context) (*probe.Probe, error) { return nil, errors.New("not opened") }

type richAttachment struct{ AttachmentInfo }

func (a richAttachment) Info() AttachmentInfo { return a.AttachmentInfo }

func TestTransportOrderingWithSerialCollisions(t *testing.T) {
	input := []Attachment{
		richAttachment{AttachmentInfo{Key: "0"}},
		richAttachment{AttachmentInfo{Serial: "A", Key: "1"}},
		richAttachment{AttachmentInfo{Serial: "a", Key: "2"}},
		richAttachment{AttachmentInfo{Serial: "a", Location: "1", Key: "3"}},
		richAttachment{AttachmentInfo{Serial: "a", Location: "2", Product: "a", Key: "4"}},
		richAttachment{AttachmentInfo{Serial: "a", Location: "2", Product: "b", Key: "5"}},
		richAttachment{AttachmentInfo{Serial: "a", Location: "2", Product: "b", Key: "6"}},
	}
	var r Registry
	if err := r.RegisterTransport(NewTransportProvider("usb", func(context.Context) ([]Attachment, error) { return input, nil })); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 16 {
		rng.Shuffle(len(input), func(i, j int) { input[i], input[j] = input[j], input[i] })
		i, err := r.Transports(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for n, a := range slices.Collect(iter.Seq[Transport](i)) {
			if a.Info().Key != string(rune('0'+n)) {
				t.Fatalf("order at %d: %v", n, a.Info())
			}
		}
	}
}
