package store

// Related memories: the ones that look like they are about the same thing as a
// new fact without being the same fact. remember shows them so the model can
// replace a stale memory instead of keeping both (v0.7 proactive memory).

// Related returns up to limit memories whose word overlap with text is at least
// lo but below Near (Near and above is already merged by Add), best first.
// skip is a memory id to leave out (the one just saved).
func (d *DB) Related(text string, lo float64, limit int, skip int64) []Memory {
	n := Norm(text)
	var out []Memory
	var score []float64
	for _, m := range d.Search(text, 12) {
		if m.ID == skip {
			continue
		}
		s := similar(n, Norm(m.Text))
		if s < lo || s >= Near {
			continue
		}
		i := len(out)
		out = append(out, m)
		score = append(score, s)
		for i > 0 && score[i] > score[i-1] { // keep best first
			out[i], out[i-1] = out[i-1], out[i]
			score[i], score[i-1] = score[i-1], score[i]
			i--
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
