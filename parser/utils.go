package parser

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/url"
	"regexp"
	"strings"

	"github.com/Polymail/go-falcon/iconv"
	"github.com/Polymail/go-falcon/utils"
	"github.com/sloonz/go-qprintable"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

var (
	invalidUnquotedRE    = regexp.MustCompile(`(.)*\s(filename|name)=[^"](.+\s)+[^"]`)
	invalidUnquotedResRE = regexp.MustCompile(`[^=]+$`)
	invalidEscapedRE     = regexp.MustCompile(`name\*[[0-9]*]?=iso-2022-jp'ja'(.*)`)
	mimeHeaderRE         = regexp.MustCompile(`=\?(.+?)\?([QBqb])\?(.+?)\?=`)
	mimeSpacesHeaderRE   = regexp.MustCompile(`(\?=)\s*(=\?)`)
	fixCharsetRE         = regexp.MustCompile(`[_:.\/\\]`)
	invalidContentIdRE   = regexp.MustCompile(`<(.*)>`)
	invalidFromToRE      = regexp.MustCompile(`(.*)<(.*)>`)
)

// fix escaped and unquoted headers values

func FixMailEncodedHeader(str string) string {
	return fixInvalidEscapedAttachmentName(fixInvalidUnquotedAttachmentName(str))
}

func fixInvalidUnquotedAttachmentName(str string) string {
	if invalidUnquotedRE.MatchString(str) {
		str = invalidUnquotedResRE.ReplaceAllString(str, "\"$0\"")
	}
	return str
}

func fixInvalidEscapedAttachmentName(str string) string {
	var words []string
	arrayStr := strings.Split(str, " ")
	for _, word := range arrayStr {
		if invalidEscapedRE.MatchString(word) {
			unescapedStr, err := url.QueryUnescape(word)
			if err == nil {
				sr := strings.NewReader(unescapedStr)
				tr, err := ioutil.ReadAll(transform.NewReader(sr, japanese.ISO2022JP.NewDecoder()))
				if err == nil {
					unescapedStr = string(tr)
				}
				unescapedStr = invalidEscapedRE.ReplaceAllString(unescapedStr, "name=\"$1\"")
				word = unescapedStr
			}
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}

// encode Mime

func MimeHeaderDecode(str string) string {
	str = collapseAdjacentEncodings(str)
	for _, word := range mimeHeaderRE.FindAllStringSubmatch(str, -1) {
		if len(word) > 2 {
			switch strings.ToUpper(word[2]) {
			case "B":
				str = strings.Replace(str, word[0], FixEncodingAndCharsetOfPart(word[3], "base64", word[1], true), 1)
			case "Q":
				str = strings.Replace(str, word[0], FixEncodingAndCharsetOfPart(strings.Replace(word[3], "_", " ", -1), "quoted-printable", word[1], true), 1)
			}
		}
	}
	return str
}

func collapseAdjacentEncodings(str string) string {
	var (
		resData                             []string
		encoding, prevEncoding, lastElement string
	)

	stringSplitted := mimeSpacesHeaderRE.Split(str, -1)
	if len(stringSplitted) > 1 {
		// fix split
		for i, word := range stringSplitted {
			switch i {
			case 0:
				stringSplitted[i] = word + "?="
			case (len(stringSplitted) - 1):
				stringSplitted[i] = "=?" + word
			default:
				stringSplitted[i] = "=?" + word + "?="
			}
		}
		// When the encoded string consists of multiple lines, lines with the same
		// encoding (Q or B) can be joined together.
		for _, word := range stringSplitted {
			matched := mimeHeaderRE.FindAllStringSubmatch(word, 1)
			if len(matched) > 0 && len(matched[0]) > 2 {
				encoding = strings.ToUpper(matched[0][2])
				if encoding == prevEncoding {
					if len(resData) > 0 {
						lastElement, resData = resData[len(resData)-1], resData[:len(resData)-1]
						word = lastElement + word
					}
				}
				prevEncoding = encoding
			}
			resData = append(resData, word)
		}
		// return string
		return strings.Join(resData, " ")
	} else {
		return str
	}
}

// fix email body

func FixEncodingAndCharsetOfPart(data, contentEncoding, contentCharset string, checkOnInvalidUtf bool) string {
	// encoding
	switch contentEncoding {
	case "quoted-printable":
		data = fromQuotedP(data)
	case "base64":
		data = utils.DecodeBase64(data)
	}

	// charset
	if contentCharset == "" {
		contentCharset = "utf-8"
	} else {
		contentCharset = strings.ToLower(contentCharset)
	}

	if contentCharset != "utf-8" && contentCharset != "utf8" && contentCharset != "us-ascii" {
		switch contentCharset {
		case "7bit", "8bit":
			return data
		case "iso-8859-1":
			b := new(bytes.Buffer)
			for _, c := range []byte(data) {
				b.WriteRune(rune(c))
			}
			return b.String()
		case "shift_jis", "shift-jis", "iso-2022-jp", "big5", "gb2312", "gb18030", "gbk", "iso-8859-2", "iso-8859-6", "iso-8859-8", "iso-8859-15", "koi8-r", "koi8-u", "windows-1250", "windows-1251", "windows-1252", "windows-1256", "euc-kr":

			var decoder transform.Transformer

			switch contentCharset {
			case "iso-2022-jp":
				decoder = japanese.ISO2022JP.NewDecoder()
			case "big5":
				decoder = traditionalchinese.Big5.NewDecoder()
			case "gb18030":
				decoder = simplifiedchinese.GB18030.NewDecoder()
			case "gbk", "gb2312":
				decoder = simplifiedchinese.GBK.NewDecoder()
			case "iso-8859-2":
				decoder = charmap.ISO8859_2.NewDecoder()
			case "iso-8859-6":
				decoder = charmap.ISO8859_6.NewDecoder()
			case "iso-8859-8":
				decoder = charmap.ISO8859_8.NewDecoder()
			case "iso-8859-15":
				decoder = charmap.ISO8859_15.NewDecoder()
			case "koi8-r":
				decoder = charmap.KOI8R.NewDecoder()
			case "koi8-u":
				decoder = charmap.KOI8U.NewDecoder()
			case "windows-1250":
				decoder = charmap.Windows1250.NewDecoder()
			case "windows-1251":
				decoder = charmap.Windows1251.NewDecoder()
			case "windows-1252":
				decoder = charmap.Windows1252.NewDecoder()
			case "windows-1256":
				decoder = charmap.Windows1256.NewDecoder()
			case "euc-kr":
				decoder = korean.EUCKR.NewDecoder()
			case "shift_jis", "shift-jis":
				decoder = japanese.ShiftJIS.NewDecoder()
			default:
				panic(fmt.Sprintf("programmer: unknown charset (%v)", contentCharset))
			}
			tr, err := ioutil.ReadAll(transform.NewReader(strings.NewReader(data), decoder))
			if err == nil {
				data = string(tr)
			} else {
				convstr, err := convertByIconv(data, contentCharset)
				if err == nil {
					data = convstr
				}
			}
		default:
			convstr, err := convertByIconv(data, contentCharset)
			if err == nil {
				data = convstr
			}
		}
	}
	// valid utf
	if checkOnInvalidUtf {
		data = utils.CheckAndFixUtf8(data)
	}
	// result
	return data
}

func convertByIconv(data, contentCharset string) (string, error) {
	converter, err := iconv.Open("UTF-8", strings.ToUpper(fixCharset(contentCharset)))
	if err != nil {
		return data, err
	}
	defer converter.Close()
	convertedString := converter.ConvString(data)
	if convertedString == "" {
		return data, nil
	} else {
		return convertedString, nil
	}
}

// quoted-printable

func fromQuotedP(data string) string {
	decoder := qprintable.NewDecoder(qprintable.BinaryEncoding, bytes.NewBufferString(data))
	res, _ := ioutil.ReadAll(decoder)
	return string(res)
}

// fix charset

func fixCharset(charset string) string {
	fixedCharset := fixCharsetRE.ReplaceAllString(charset, "-")
	// Fix charset
	// borrowed from http://squirrelmail.svn.sourceforge.net/viewvc/squirrelmail/trunk/squirrelmail/include/languages.php?revision=13765&view=markup
	// OE ks_c_5601_1987 > cp949
	fixedCharset = strings.Replace(fixedCharset, "ks-c-5601-1987", "cp949", -1)
	// Moz x-euc-tw > euc-tw
	fixedCharset = strings.Replace(fixedCharset, "x-euc", "euc", -1)
	// Moz x-windows-949 > cp949
	fixedCharset = strings.Replace(fixedCharset, "x-windows_", "cp", -1)
	// windows-125x and cp125x charsets
	fixedCharset = strings.Replace(fixedCharset, "windows-", "cp", -1)
	// ibm > cp
	fixedCharset = strings.Replace(fixedCharset, "ibm", "cp", -1)
	// iso-8859-8-i -> iso-8859-8
	fixedCharset = strings.Replace(fixedCharset, "iso-8859-8-i", "iso-8859-8", -1)
	if charset != fixedCharset {
		return fixedCharset
	}
	return charset
}

// invalid content ID

func getInvalidContentId(contentId string) string {
	if invalidContentIdRE.MatchString(contentId) {
		res := invalidContentIdRE.FindStringSubmatch(contentId)
		if len(res) == 2 {
			contentId = res[1]
		}
	}
	return contentId
}

// invalid from/to email

func getInvalidFromToHeader(header string) (string, string) {
	if invalidFromToRE.MatchString(header) {
		res := invalidFromToRE.FindStringSubmatch(header)
		if len(res) == 3 {
			return MimeHeaderDecode(strings.TrimSpace(res[1])), res[2]
		} else {
			return "", res[2]
		}
	}
	return "", header
}
