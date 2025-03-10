package coordinator

import (
	"bufio"
	"github.com/Depermitto/psirent/filedistrib/coms"
	"github.com/Depermitto/psirent/filedistrib/persistent"
	"io"
	"net"
)

func Ls(pw io.Writer, storage persistent.Storage) (int, error) {
	bufpw := bufio.NewWriter(pw)
	available := 0
	for filehash, ips := range storage {
		hashSeen := false
		for i, ip := range ips {
			if conn, err := net.Dial("tcp4", ip); err == nil {
				if Has(conn, filehash) {
					// write only once
					if !hashSeen {
						if available > 0 {
							_, _ = bufpw.WriteString(coms.LsSeparator)
						}
						_, _ = bufpw.WriteString(filehash)
						available += 1
						hashSeen = true
					}
				} else {
					persistent.Remove(storage, filehash, i)
				}
				conn.Close()
			}
		}
	}
	_, _ = bufpw.WriteString("\n")
	return available, bufpw.Flush()
}
