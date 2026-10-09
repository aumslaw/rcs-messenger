package leader

type Checker interface {
	IsLeader() bool
}
