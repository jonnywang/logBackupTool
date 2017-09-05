package logBackup

import (
	"net"
	"fmt"
	"time"
	"errors"
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"strconv"
	"io"
	"path"
	"regexp"
)

type Server struct {
	addr       string
	socket     *net.TCPListener
	cm         *ConnManager
}

func NewServer() (*Server, error) {
	Debugf("server will running at %s", GloablConfig.Addr)
	return &Server{
		addr       : GloablConfig.Addr,
		socket     : nil,
		cm         : NewConnManager(),
	}, nil
}

func (srv *Server) Start() error {
	addr, err := net.ResolveTCPAddr("tcp", srv.addr)
	if err != nil {
		return fmt.Errorf("fail to resolve addr: %v", err)
	}

	sock, err := net.ListenTCP("tcp", addr)
	if err != nil {
		Debugf("server run %s failed %s", srv.addr, err)
		return fmt.Errorf("fail to listen tcp: %v", err)
	}

	srv.socket = sock

	return srv.acceptConn()
}

func (srv *Server) Stop() error {
	srv.socket.SetDeadline(time.Now())

	tt := time.NewTimer(time.Second * time.Duration(30))
	wait := make(chan struct{})
	go func() {
		srv.cm.Wait()
		wait <- struct{}{}
	}()

	select {
	case <-tt.C:
		return errors.New("stop server wait timeout")
	case <-wait:
		return nil
	}
}

func (srv *Server)acceptConn() error {
	defer func() {
		srv.socket.Close()
	}()

	for {
		conn, err := srv.socket.Accept()
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				break;
			}
			continue
		}

		go func() {
			srv.cm.Add(1)
			srv.handleConn(conn)
			srv.cm.Done()
		}()
	}

	return nil
}

func (srv *Server) handleConn(conn net.Conn) {
	var clientAddr = conn.RemoteAddr().String()
	Debugf("%s connected", clientAddr)

	defer func() {
		Debugf("%s disconnected", clientAddr)
		conn.Close()
	}()

	data := make([]byte, 1024)
	r := bufio.NewReader(conn)

	//file size path\r\n
	//data
	for {
		conn.SetReadDeadline(time.Now().Add(time.Second * time.Duration(30)))

		content, err := r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				Debugf("%s parse transfer header failed %v", clientAddr, err)
			}
			break
		}

		summaryInfo := strings.Split(strings.Trim(content, "\r\n"), "@")
		summaryLen := len(summaryInfo)
		if summaryLen < 3 {
			if summaryInfo[0] == "PING" {
				conn.Write([]byte("PONG\r\n"))
				continue
			} else {
				Debugf("%s parse transfer header failed %s", clientAddr, content)
				conn.Write([]byte("parse transfer header failed\r\n"))
				break
			}
		}

		//文件相对路径
		fpath := summaryInfo[summaryLen-1]
		if len(fpath) > 0 {
			if matched, err := regexp.Match(`^[0-9a-zA-Z\-_/]{1,}$`, []byte(fpath)); err != nil || !matched {
				Debugf("Sorry, transfer file path is invalide\n")
				break
			}
		}

		//文件大小
		fsize := 0
		if len(summaryInfo[summaryLen-2]) > 0 {
			num, err := strconv.Atoi(summaryInfo[summaryLen-2])
			if err != nil {
				Debugf("%s parse transfer header failed %s %s", clientAddr, content, err)
				conn.Write([]byte("parse transfer header failed error size\r\n"))
				break
			}
			fsize = num
		}

		if fsize == 0 {
			Debugf("%s parse transfer header failed %s error size", clientAddr, content)
			conn.Write([]byte("parse transfer header failed error size\r\n"))
			break
		}

		//避免文件名包含@
		fname := strings.Trim(strings.Join(summaryInfo[:summaryLen-2], "@"), "@")

		//文件保存路径
		fileName := path.Join(GloablConfig.BackupPath, fpath, fname)

		Debugf("%s backup file %s size %d", clientAddr, fileName, fsize)

		parentDir := filepath.Dir(fileName)
		if _, err := os.Stat(parentDir); os.IsNotExist(err) {
			if err := os.MkdirAll(parentDir, GloablConfig.Perm); err != nil {
				Debugf("%s creaet folder %s failed %v", clientAddr, parentDir, err)
				break
			}
		}

		f, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, GloablConfig.Perm)
		if err != nil {
			Debugf("%s open file %s failed %v", clientAddr, fileName, err)
			break
		}

		m := 0
		for {
			conn.SetReadDeadline(time.Now().Add(time.Second * time.Duration(180)))
			n, err := r.Read(data)
			if err != nil {
				break;
			}

			f.Write(data[:n])
			m += n
			if m >= fsize {
				break
			}
		}

		f.Close()

		if GloablConfig.ToChown {
			go func() {
				ChownR(GloablConfig.BackupPath, GloablConfig.Uid, GloablConfig.Gid)
			}()
		}

		if m >= fsize {
			conn.Write([]byte("OK\r\n"))
		} else {
			conn.Write([]byte("ERROR\r\n"))
			break
		}
		r.Reset(conn)
	}

}