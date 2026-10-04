package platform

import (
	"fmt"
	"os"
	"strings"
)

// unsupportedEnvNames lists the environment variables mlmforge migrate refuses.
var unsupportedEnvNames = []string{
	"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "PGREALM",
	"PGREQUIRESSL", "PGSSLCRL", "PGREQUIREPEER",
	"PGKRBSRVNAME", "PGGSSLIB", "PGSYSCONFDIR", "PGLOCALEDIR",
}

// UnsupportedEnvError reports the refused environment variables that are set.
type UnsupportedEnvError struct {
	Names []string
}

func (e *UnsupportedEnvError) Error() string {
	if len(e.Names) == 1 {
		return fmt.Sprintf("mlmforge migrate refused to open the database: the environment sets %s. "+
			"mlmforge migrate does not accept %s. Unset it and run the command again.", e.Names[0], e.Names[0])
	}
	return fmt.Sprintf("mlmforge migrate refused to open the database: the environment sets %s. "+
		"mlmforge migrate does not accept these variables. Unset them and run the command again.", joinNames(e.Names))
}

// joinNames joins names as "A and B" or "A, B and C".
func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// refuseUnsupportedEnv returns an UnsupportedEnvError naming every refused variable present in the environment.
func refuseUnsupportedEnv() error {
	var set []string
	for _, name := range unsupportedEnvNames {
		if _, ok := os.LookupEnv(name); ok {
			set = append(set, name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return &UnsupportedEnvError{Names: set}
}
