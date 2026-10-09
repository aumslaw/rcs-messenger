package logg

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func Setup(debug bool) {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
}

func Info() *zerolog.Event {
	return log.Info()
}

func Debug() *zerolog.Event {
	return log.Debug()
}

func Error() *zerolog.Event {
	return log.Error()
}

func Warn() *zerolog.Event {
	return log.Warn()
}

func Fatal() *zerolog.Event {
	return log.Fatal()
}

func Logger() zerolog.Logger {
	return log.Logger
}
