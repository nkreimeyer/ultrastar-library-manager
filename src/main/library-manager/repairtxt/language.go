package repairtxt

import (
	"math"
	"sort"
)

// jmLambda is the weight of the language's own distribution in detectLanguage.
const jmLambda = 0.5

// minDetectTokens is the corpus size a language needs before detectLanguage considers it.
const minDetectTokens = 5000

// detectLanguage guesses the language of a damaged song from its undamaged words with a
// multinomial naive Bayes over the corpus languages (Jelinek-Mercer smoothed, see score). It returns "" if there are too few
// words or no language corpus is big enough. If tagged languages are given, a different
// language is only returned if it scores clearly better (by 0.5 nats per word) than the
// best of them, so a wrong tag is corrected but a right one is not diluted.
func (lx *Lexicon) detectLanguage(words []string, tagged ...string) string {
	if len(words) < 10 {
		return ""
	}
	// Jelinek-Mercer smoothing with the whole library: P(w|L) = λ·c_L(w)/N_L + (1-λ)·P(w).
	// A word a language has never seen costs the same in every language, so only words a
	// language has seen count for it. (With add-one or Dirichlet smoothing a missing word
	// costs a big corpus more than a small one, and songs with many rare words drifted
	// to the small languages.)
	g := lx.global()
	gDenom := float64(g.total + len(g.words))
	score := func(ll *langLex) float64 {
		v := 0.0
		for _, w := range words {
			v += math.Log(jmLambda*float64(ll.count(w))/float64(ll.total) + (1-jmLambda)*float64(g.count(w)+1)/gDenom)
		}
		return v
	}
	type scored struct {
		code string
		v    float64
	}
	var scores []scored
	tagBest := math.Inf(-1)
	for code, ll := range lx.langs {
		if code == "" || ll.total < minDetectTokens {
			continue
		}
		v := score(ll)
		scores = append(scores, scored{code, v})
		for _, t := range tagged {
			if t == code {
				tagBest = max(tagBest, v)
			}
		}
	}
	if len(scores) == 0 {
		return ""
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].v != scores[j].v {
			return scores[i].v > scores[j].v
		}
		return scores[i].code < scores[j].code
	})
	best := scores[0]
	if len(tagged) > 0 && (best.v-tagBest)/float64(len(words)) < 0.5 {
		return ""
	}
	return best.code
}
