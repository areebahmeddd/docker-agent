package secretsscan

// acAutomaton is an Aho–Corasick automaton matching a fixed set of
// byte patterns against an input. It serves as the keyword
// pre-filter for [Redact] and [ContainsSecrets]: a single linear
// pass over the input yields a bitset of which keywords occur, after
// which each rule can decide cheaply whether to run its (relatively
// expensive) regular expression. The previous implementation called
// [strings.Contains] once per keyword per rule — O(N · K) — which
// dominated runtime on the hot "no secret in sight" path.
//
// The automaton folds ASCII upper-case bytes to lower-case during
// the scan so callers don't need to allocate a lower-cased copy of
// the input. Patterns must therefore be supplied lower-cased.
//
// Up to 128 patterns are supported; the per-state accept set is
// stored as a fixed [2]uint64 bitset so the inner loop stays
// branch-free. Today's catalogue uses ~108 unique keywords; the
// limit is enforced at build time so a future addition that exceeds
// it surfaces as an immediate panic rather than a silent miscompare.
type acAutomaton struct {
	// next is the dense state-transition table, laid out as
	// next[state*acAlphabet + byte]. Storing it flat (rather than as
	// [][acAlphabet]int32) keeps every transition one indirection
	// away and friendlier to the CPU prefetcher.
	next []int32
	// accept[s] is the bitset of pattern indices accepted at state s,
	// already merged with the accept sets of every state reachable
	// via fail links — so the scan loop never has to walk them.
	accept [][2]uint64
}

const acAlphabet = 256

// buildAhoCorasick compiles patterns into an automaton. Patterns
// must be lower-cased ASCII; the [acAutomaton.scan] method folds the
// input on the fly so we don't need a lower-cased copy.
func buildAhoCorasick(patterns []string) *acAutomaton {
	if len(patterns) > 128 {
		panic("secretsscan: too many AC patterns for [2]uint64 accept bitset")
	}
	type node struct {
		children map[byte]int32
		fail     int32
		accept   [2]uint64
	}
	nodes := []*node{{children: map[byte]int32{}}}
	for idx, p := range patterns {
		cur := int32(0)
		for i := range len(p) {
			c := p[i]
			child, ok := nodes[cur].children[c]
			if !ok {
				child = int32(len(nodes))
				nodes = append(nodes, &node{children: map[byte]int32{}})
				nodes[cur].children[c] = child
			}
			cur = child
		}
		nodes[cur].accept[idx>>6] |= 1 << uint(idx&63)
	}

	// BFS over the trie to compute fail links and propagate accept
	// bits along them. Children of the root always fail back to the
	// root itself.
	queue := make([]int32, 0, len(nodes))
	for _, child := range nodes[0].children {
		nodes[child].fail = 0
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		s := queue[head]
		for c, u := range nodes[s].children {
			f := nodes[s].fail
			for {
				if v, ok := nodes[f].children[c]; ok && v != u {
					nodes[u].fail = v
					break
				}
				if f == 0 {
					nodes[u].fail = 0
					break
				}
				f = nodes[f].fail
			}
			nodes[u].accept[0] |= nodes[nodes[u].fail].accept[0]
			nodes[u].accept[1] |= nodes[nodes[u].fail].accept[1]
			queue = append(queue, u)
		}
	}

	// Materialise the dense delta(state, byte) table. For every byte
	// not present as a child of state, follow the fail chain to find
	// a state that does have it, defaulting to root.
	next := make([]int32, len(nodes)*acAlphabet)
	accept := make([][2]uint64, len(nodes))
	for i, n := range nodes {
		accept[i] = n.accept
		for c := range acAlphabet {
			s := int32(i)
			for {
				if v, ok := nodes[s].children[byte(c)]; ok {
					next[i*acAlphabet+c] = v
					break
				}
				if s == 0 {
					next[i*acAlphabet+c] = 0
					break
				}
				s = nodes[s].fail
			}
		}
	}
	return &acAutomaton{next: next, accept: accept}
}

// scan returns a bitset of every pattern index that occurs at least
// once in text. Bytes 'A'..'Z' are folded to 'a'..'z' on the fly so
// callers do not have to lower-case the input first.
func (a *acAutomaton) scan(text string) (mask [2]uint64) {
	s := int32(0)
	for i := range len(text) {
		c := text[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		s = a.next[int(s)*acAlphabet+int(c)]
		mask[0] |= a.accept[s][0]
		mask[1] |= a.accept[s][1]
	}
	return mask
}
