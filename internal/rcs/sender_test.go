package rcs

import "github.com/zagnu-labs/rcs-messenger/internal/providers/rcsphone"

var _ sender = (*rcsphone.Sender)(nil)
