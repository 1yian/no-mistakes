package config

func (p PR) PublishesIntent() bool {
	return p.PublishIntent == nil || *p.PublishIntent
}
