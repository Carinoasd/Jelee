package images

// LiveVariants counts the variants the live generation indexes, for the
// image-variants repair action (G50.4). ClearVariants is the action itself.
func (s *Store) LiveVariants() (entries, bytes int64) {
	if s == nil {
		return 0, 0
	}
	stats := s.Stats()
	return int64(stats.VariantEntries), stats.VariantBytes
}
