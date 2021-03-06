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
	"crypto/md5"
	"encoding/hex"
)

type Server struct {
	addr       string
	socket     *net.TCPListener
	cm         *ConnManager
}

func NewServer() (*Server, error) {
	Logger.Printf("server will running at %s", GloablConfig.Addr)
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
		Logger.Printf("server run %s failed %s", srv.addr, err)
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

		go func(conn net.Conn) {
			srv.cm.Add(1)
			srv.handleConn(&FConn{conn:conn})
			srv.cm.Done()
		}(conn)
	}

	return nil
}

func (srv *Server) handleConn(conn *FConn) {
	var clientAddr = conn.ClientAddress()
	Logger.Printf("%s connected", clientAddr)

	defer func() {
		Logger.Printf("%s disconnected", clientAddr)
		err := conn.Close()
		if err != nil {
			Logger.Printf("%s disconnected error %s", clientAddr, err)
		}
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
				Logger.Printf("%s parse transfer header failed %v", clientAddr, err)
				conn.Write([]byte("ERROR\r\n"))
			}
			break
		}

		//ping     PING@
		//transfer FILE_NAME@FILE_SIZE@md5sum@PATH
		summaryInfo := strings.Split(strings.Trim(content, "\r\n"), "@")
		summaryLen := len(summaryInfo)
		if summaryLen < 4 {
			if summaryInfo[0] == "PING" {
				conn.Write([]byte("PONG\r\n"))
				continue
			} else {
				Logger.Printf("%s parse transfer header failed %s", clientAddr, content)
				conn.Write([]byte("parse transfer header failed\r\n"))
				break
			}
		}

		//文件相对路径
		fpath := summaryInfo[summaryLen-1]
		if len(fpath) > 0 {
			if matched, err := regexp.Match(`^[0-9a-zA-Z\-_/]{1,}$`, []byte(fpath)); err != nil || !matched {
				Logger.Printf("Sorry, transfer file path is invalid\n")
				conn.Write([]byte("ERROR\r\n"))
				break
			}
		}

		//文件md5校验码
		fsum := summaryInfo[summaryLen-2]

		//文件大小
		fsize := 0
		if len(summaryInfo[summaryLen-3]) > 0 {
			num, err := strconv.Atoi(summaryInfo[summaryLen-3])
			if err != nil {
				Logger.Printf("%s parse transfer header failed %s %s", clientAddr, content, err)
				conn.Write([]byte("parse transfer header failed error size\r\n"))
				break
			}
			fsize = num
		}

		if fsize == 0 {
			Logger.Printf("%s parse transfer header failed %s error size", clientAddr, content)
			conn.Write([]byte("parse transfer header failed error size\r\n"))
			break
		}

		//避免文件名包含@
		fname := strings.Trim(strings.Join(summaryInfo[:summaryLen-3], "@"), "@")

		//文件保存路径
		fileName := path.Join(GloablConfig.BackupPath, fpath, fname)

		Logger.Printf("%s backup file %s size %d md5sum %s", clientAddr, fileName, fsize, fsum)

		parentDir := filepath.Dir(fileName)
		if _, err := os.Stat(parentDir); os.IsNotExist(err) {
			if err := os.MkdirAll(parentDir, GloablConfig.Perm); err != nil {
				Logger.Printf("%s create folder %s failed %v", clientAddr, parentDir, err)
				conn.Write([]byte("ERROR\r\n"))
				break
			}
		}

		fi, err := os.Stat(fileName)
		if err == nil && fi.Size() > 0 {
			t, err := os.Open(fileName)
			if err == nil {
				h := md5.New()
				if _, err := io.Copy(h, t); err != nil {
					t.Close()
					Logger.Printf("%s Get file %s md5sum failed %v", clientAddr, fileName, err)
					conn.Write([]byte("ERROR\r\n"))
					break
				}

				ms := hex.EncodeToString(h.Sum(nil))
				Logger.Printf("%s Get file %s md5sum %s", clientAddr, fileName, ms)

				t.Close()
				if ms == fsum {
					Logger.Printf("%s Transfer file %s has same md5sum %s", clientAddr, fileName, ms)
					conn.Write([]byte("ALL_SAME\r\n"))
					continue;
				} else {
					Logger.Printf("%s Stat file %s md5sum not same and will transfer file data", clientAddr, fileName)
				}
			} else {
				Logger.Printf("%s Stat file %s failed %v and will transfer file data", clientAddr, fileName, err)
			}
		} else {
			Logger.Printf("%s Stat file %s failed %v and will transfer file data", clientAddr, fileName, err)
		}

		conn.Write([]byte("CONTINUE\r\n"))

		tmpFileName := fmt.Sprintf("%s.tmp", fileName)
		f, err := os.OpenFile(tmpFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, GloablConfig.Perm)
		if err != nil {
			Logger.Printf("%s open file %s failed %v", clientAddr, tmpFileName, err)
			conn.Write([]byte("ERROR\r\n"))
			break
		}

		h := md5.New()
		m := 0
		for {
			conn.SetReadDeadline(time.Now().Add(time.Second * time.Duration(180)))
			n, err := r.Read(data)
			if err != nil {
				break;
			}

			h.Write(data[:n])
			f.Write(data[:n])
			m += n
			if m >= fsize {
				break
			}
		}

		ms := hex.EncodeToString(h.Sum(nil))

		f.Close()

		err = os.Rename(tmpFileName, fileName)
		if err != nil {
			Logger.Printf("%s Rename file %s failed %v", clientAddr, tmpFileName, err)
			conn.Write([]byte("ERROR\r\n"))
			break
		}

		if GloablConfig.ToChown {
			go func() {
				ChownR(GloablConfig.BackupPath, GloablConfig.Uid, GloablConfig.Gid)
			}()
		}

		if m == fsize && ms == fsum {
			Logger.Printf("%s Transfer file %s success md5sum %s size %d", clientAddr, fileName, ms, m)
			conn.Write([]byte("OK\r\n"))
		} else {
			Logger.Printf("%s Transfer file %s failed md5sum %s size %d", clientAddr, fileName, ms, m)
			conn.Write([]byte("ERROR\r\n"))
			break
		}
	}
}