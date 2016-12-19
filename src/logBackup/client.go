package logBackup

import (
	"net"
	"fmt"
	"os"
	"io"
	"strings"
)

func Transerf(server string, file string, relative_path string) error {
	fi, err := os.Stat(file)
	if err != nil || fi.IsDir() {
		fmt.Printf("Sorry,not found transfer file %s\n", file)
		os.Exit(1)
	}

	f, err := os.Open(file)
	if err != nil {
		Debugf("Open file %s failed %v", file, err)
		return err
	}
	defer f.Close();

	conn, err := net.Dial("tcp", server)
	if err != nil {
		Debugf("Connect target server %s failed %v", server, err)
		return err
	}

	defer conn.Close()
	conn.Write([]byte(fmt.Sprintf("%s %d %s\r\n", fi.Name(), fi.Size(), relative_path)))
	io.Copy(conn, f);

	buff := make([]byte, 1024)
	n, err := conn.Read(buff)
	if err == nil {
		fmt.Printf("Server Response %s\n", strings.Trim(string(buff[:n]), "\r\n"))
	}

	return nil
}