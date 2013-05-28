package parser

import (
  "bytes"
  "net/mail"
  "mime"
  "mime/multipart"
  "io"
  "io/ioutil"
  "strings"
  "time"
  "github.com/le0pard/go-falcon/log"
  "github.com/le0pard/go-falcon/protocol/smtpd"
)

type ParsedAttachment struct {
  attachmentType            string
  attachmentFileName        string
  attachmentContentType     string
  attachmentBody            []byte
}

type ParsedEmail struct {
  env *smtpd.BasicEnvelope

  RawMail       []byte

  Subject       string
  Date          time.Time
  From          mail.Address
  To            mail.Address
  Headers       mail.Header

  HtmlPart      string
  TextPart      string

  Attachments   []ParsedAttachment

  EmailBody     []byte
}

// parse headers

func (email *ParsedEmail) parseEmailHeaders(msg *mail.Message) {
  var emailHeader = ""
  var err error

  email.Headers = msg.Header
  email.Subject = email.Headers.Get("Subject")
  email.Date, err = msg.Header.Date()
  if err != nil {
    log.Errorf("Failed parsing date: %v", err)
    email.Date = time.Now()
  }
  // from
  emailHeader = email.Headers.Get("From")
  if emailHeader != "" {
    fromEmail, err := mail.ParseAddress(emailHeader)
    if err != nil {
      email.From = mail.Address{ Address: email.env.From.Email() }
    } else {
      email.From = *fromEmail
    }
  }
  // to
  emailHeader = email.Headers.Get("To")
  if emailHeader != "" {
    toEmail, err := mail.ParseAddress(emailHeader)
    if err != nil {
      email.To = mail.Address{}
    } else {
      email.To = *toEmail
    }
  }
}

// select type of email

func (email *ParsedEmail) parseEmailByType(contentType string, contentDisposition string, pbody []byte) {
  if contentType == "" {
    contentType = "text/plain; charset=UTF-8"
  }
  contentTypeVal, contentTypeParams, err := mime.ParseMediaType(contentType)
  if err != nil {
    log.Errorf("Invalid ContentType: %v", err)
    return
  }
  if contentDisposition != "" {
    contentDispositionVal, contentDispositionParams, err := mime.ParseMediaType(contentDisposition)
    if err != nil {
      log.Errorf("Invalid ContentDisposition: %v", err)
      return
    }
    switch strings.ToLower(contentDispositionVal) {
    case "attachment", "inline":
      attachment := ParsedAttachment{ attachmentType: contentDispositionVal, attachmentFileName: contentDispositionParams["filename"], attachmentBody: pbody, attachmentContentType: contentTypeVal }
      email.Attachments = append(email.Attachments, attachment)
    default:
      log.Errorf("Unknown content disposition: %s", contentDispositionVal)
      log.Errorf("Unknown content params: %v", contentDispositionParams)
    }
  } else {
    switch strings.ToLower(contentTypeVal) {
    case "text/html":
      email.HtmlPart = string(pbody)
    case "text/plain":
      email.TextPart = string(pbody)
    default:
      if strings.HasPrefix(strings.ToLower(contentTypeVal), "multipart/") {
        email.parseMimeEmail(pbody, contentTypeParams["boundary"])
      } else {
        log.Errorf("Unknown content type: %s", contentTypeVal)
        log.Errorf("Unknown content params: %v", contentTypeParams)
        log.Errorf("Unknown content: %v", string(pbody))
      }
    }
  }
}

// parse part of email

func (email *ParsedEmail) parseEmailPart(part *multipart.Part) {
  pbody, err := ioutil.ReadAll(part)
  if err != nil {
    log.Errorf("Read part: %v", err)
    return
  }
  contentType := part.Header.Get("Content-Type")
  contentDisposition := part.Header.Get("Content-Disposition")
  email.parseEmailByType(contentType, contentDisposition, pbody)
}

// parse plain email

func (email *ParsedEmail) parsePlainEmail() {
  contentType := email.Headers.Get("Content-Type")
  contentDisposition := email.Headers.Get("Content-Disposition")
  email.parseEmailByType(contentType, contentDisposition, email.EmailBody)
}

// parse plain email

func (email *ParsedEmail) parseMimeEmail(pbody []byte, boundary string) {
  if boundary == "" {
    log.Errorf("Doesn't found boundary in MIME: %s", boundary)
    return
  }

  bodyReader := bytes.NewReader(pbody)
  reader := multipart.NewReader(bodyReader, boundary)

  for {
    p, err := reader.NextPart()
    if err == io.EOF {
      break
    }
    if err != nil {
      log.Errorf("Mime Part error: %v", err)
    } else {
      email.parseEmailPart(p)
    }
  }
}

// parse body

func (email *ParsedEmail) parseEmailBody(msg *mail.Message, body []byte) {
  email.EmailBody = body
  mimeVersion := email.Headers.Get("Mime-Version")
  if mimeVersion != "" {
    contentType := email.Headers.Get("Content-Type")
    _, contentTypeParams, err := mime.ParseMediaType(contentType)
    if err != nil {
      log.Errorf("Invalid ContentType: %v", err)
      return
    }
    email.parseMimeEmail(email.EmailBody, contentTypeParams["boundary"])
  } else {
    email.parsePlainEmail()
  }
}

// store email

func (email *ParsedEmail) storeEmail() {
  log.Debugf("HTML Email: %v", email.HtmlPart)
  log.Debugf("Text Email: %v", email.TextPart)

}

// obj

type EmailParser struct {
}

// parse email

func (parser *EmailParser) ParseMail(env *smtpd.BasicEnvelope) (*ParsedEmail, error) {
  email := ParsedEmail{ env: env }
  msg, err := mail.ReadMessage(bytes.NewBuffer(email.env.MailBody))
  if err != nil {
    log.Errorf("Failed parsing message: %v", err)
    return nil, err
  }
  mailBody, err := ioutil.ReadAll(msg.Body)
  if err != nil {
    log.Errorf("Failed parsing message: %v", err)
    return nil, err
  }
  email.RawMail = email.env.MailBody
  email.parseEmailHeaders(msg)
  email.parseEmailBody(msg, mailBody)
  return &email, nil
}
