// Package pop3 implements an pop3 server. Hooks are provided to customize
// its behavior.
// TODO: NOT FINISHED
package pop3

import (
  "bufio"
  "errors"
  "regexp"
  "fmt"
  "net"
  "os/exec"
  "crypto/tls"
  "strings"
  "time"
  "unicode"
  "github.com/le0pard/go-falcon/log"
  "github.com/le0pard/go-falcon/config"
  "github.com/le0pard/go-falcon/storage"
  "github.com/le0pard/go-falcon/utils"
)

var (
  rcptToRE = regexp.MustCompile(`[Tt][Oo]:[\s*]?<(.+)>`)
  mailFromRE = regexp.MustCompile(`[Ff][Rr][Oo][Mm]:[\s*]?<(.*)>`)
)

// Server is an SMTP server.
type Server struct {
  Addr         string // TCP address to listen on, ":2525" if empty
  Hostname     string // optional Hostname to announce; "" to use system hostname
  ReadTimeout  time.Duration  // optional read timeout
  WriteTimeout time.Duration  // optional write timeout

  TLSconfig *tls.Config // tls config

  ServerConfig *config.Config
  DBConn       *storage.DBConn

  // OnNewConnection, if non-nil, is called on new connections.
  // If it returns non-nil, the connection is closed.
  OnNewConnection func(c Connection) error
}

// Connection is implemented by the SMTP library and provided to callers
// customizing their own Servers.
type Connection interface {
  Addr() net.Addr
}

// SERVER

func (srv *Server) hostname() string {
  if srv.Hostname != "" {
    return srv.Hostname
  }
  out, err := exec.Command("hostname").Output()
  if err != nil {
    return ""
  }
  return strings.TrimSpace(string(out))
}

// ListenAndServe listens on the TCP network address srv.Addr and then
// calls Serve to handle requests on incoming connections.  If
// srv.Addr is blank, ":25" is used.
func (srv *Server) ListenAndServe() error {
  addr := srv.Addr
  if addr == "" {
    addr = ":110"
  }
  ln, e := net.Listen("tcp", addr)
  if e != nil {
    return e
  }
  return srv.Serve(ln)
}

func (srv *Server) Serve(ln net.Listener) error {
  defer ln.Close()
  for {
    rw, e := ln.Accept()
    if e != nil {
      if ne, ok := e.(net.Error); ok && ne.Temporary() {
        log.Errorf("pop3: Accept error: %v", e)
        continue
      }
      return e
    }
    sess, err := srv.newSession(rw)
    if err != nil {
      continue
    }
    go sess.serve()
  }
  panic("not reached")
}

// SESSION

type session struct {
  srv *Server
  rwc net.Conn
  br  *bufio.Reader
  bw  *bufio.Writer

  authPlain bool // bool for 2 step plain auth
  authLogin bool // bool for 2 step login auth
  authCramMd5Login string // bytes for cram-md5 login

  mailboxId     int    // id of mailbox
  authUsername  string // auth login
  authPassword  string // auth password
}

func (srv *Server) newSession(rwc net.Conn) (s *session, err error) {
  s = &session{
    srv: srv,
    rwc: rwc,
    br:  bufio.NewReader(rwc),
    bw:  bufio.NewWriter(rwc),
    authPlain: false,
    authLogin: false,
    authCramMd5Login: "",
    mailboxId: 0,
  }
  return
}

func (s *session) errorf(format string, args ...interface{}) {
  log.Errorf("Client error: "+format, args...)
}

func (s *session) sendf(format string, args ...interface{}) {
  if s.srv.WriteTimeout != 0 {
    s.rwc.SetWriteDeadline(time.Now().Add(s.srv.WriteTimeout))
  }
  fmt.Fprintf(s.bw, format, args...)
  s.bw.Flush()
}

func (s *session) sendlinef(format string, args ...interface{}) {
  s.sendf(format+"\r\n", args...)
}

func (s *session) sendPOP3ErrorOrLinef(err error, format string, args ...interface{}) {
  if se, ok := err.(POP3Error); ok {
    s.sendlinef("%s", se.Error())
    return
  }
  s.sendlinef(format, args...)
}

func (s *session) Addr() net.Addr {
  return s.rwc.RemoteAddr()
}

// parse commands to server

func (s *session) serve() {
  defer s.rwc.Close()
  if onc := s.srv.OnNewConnection; onc != nil {
    if err := onc(s); err != nil {
      s.sendPOP3ErrorOrLinef(err, "-ERR connection rejected")
      return
    }
  }
  s.clearAuthData()
  s.authCramMd5Login = utils.GenerateSMTPCramMd5(s.srv.hostname())
  s.sendf("+OK POP3 server ready %s\r\n", s.authCramMd5Login)
  for {
    if s.srv.ReadTimeout != 0 {
      s.rwc.SetReadDeadline(time.Now().Add(s.srv.ReadTimeout))
    }
    sl, err := s.br.ReadSlice('\n')
    if err != nil {
      s.errorf("read error: %v", err)
      return
    }
    line := cmdLine(string(sl))
    if err := line.checkValid(); err != nil {
      s.sendlinef("-ERR %v", err)
      continue
    }

    log.Debugf("Command from client %s", line)

    switch line.Verb() {
    case "USER":
      s.handleLoginUser(line.Arg())
    case "PASS":
      s.handleLoginPass(line.Arg())
    case "QUIT":
      s.sendlinef("+OK Bye")
      return
    case "STARTTLS":
      s.handleStartTLS()
    default:
      log.Errorf("Client: %q, verhb: %q", line, line.Verb())
      s.sendlinef("-ERR command not recognized")
    }

  }
}

// handle login user

func (s *session) handleLoginUser(line string) {
  s.authUsername = line
  s.sendlinef("+OK %s is a real", s.authUsername)
}

// handle pass user

func (s *session) handleLoginPass(line string) {
  s.authPassword = line
  if s.authUsername != "" && s.authPassword != "" {
    s.authByDB(utils.AUTH_PLAIN)
  } else {
    s.sendlinef("-ERR invalid username or password")
  }
  s.clearAuthData()
}

// auth by DB

func (s *session) authByDB(authMethod string) {
  var err error
  s.mailboxId, err = s.srv.DBConn.CheckUser(authMethod, s.authUsername, s.authPassword, s.authCramMd5Login)
  if err != nil {
    s.sendlinef("-ERR invalid username or password")
    return
  }
  s.sendlinef("+OK maildrop locked and ready")
}

// clear auth

func (s *session) clearAuthData() {
  s.authPlain = false
  s.authLogin = false
  s.authCramMd5Login = ""
  s.authUsername = ""
  s.authPassword = ""
}

// handle StartTLS

func (s *session) handleStartTLS() {
  if s.srv.ServerConfig.Adapter.Tls {
    s.sendlinef("+OK Ready to start TLS")
    var tlsConn *tls.Conn
    tlsConn = tls.Server(s.rwc, s.srv.TLSconfig)
    err := tlsConn.Handshake()
    if err != nil {
      log.Errorf("Could not TLS handshake:%v", err)
    } else {
      s.rwc = net.Conn(tlsConn)
      s.br = bufio.NewReader(s.rwc)
      s.bw = bufio.NewWriter(s.rwc)
    }
    s.sendlinef("")
  } else {
    s.sendlinef("-ERR Tsl not supported")
  }
}

// check auth if need

func (s *session) checkNeedAuth() bool {
  if s.mailboxId == 0 {
    s.sendlinef("-ERR permission denied")
    return true
  }
  return false
}

// Handle error

func (s *session) handleError(err error) {
  if se, ok := err.(POP3Error); ok {
    s.sendlinef("%s", se)
    return
  }
  log.Errorf("Error: %s", err)
}

// COMMAND LINE

type cmdLine string

func (cl cmdLine) checkValid() error {
  if !strings.HasSuffix(string(cl), "\r\n") {
    return errors.New(`line doesn't end in \r\n`)
  }
  return nil
}

func (cl cmdLine) Verb() string {
  s := string(cl)
  if idx := strings.Index(s, " "); idx != -1 {
    return strings.ToUpper(s[:idx])
  }
  return strings.ToUpper(s[:len(s)-2])
}

func (cl cmdLine) Arg() string {
  s := string(cl)
  if idx := strings.Index(s, " "); idx != -1 {
    return strings.TrimRightFunc(s[idx+1:len(s)-2], unicode.IsSpace)
  }
  return ""
}

func (cl cmdLine) String() string {
  return string(cl)
}

// ERRORS

type POP3Error string

func (e POP3Error) Error() string {
  return string(e)
}