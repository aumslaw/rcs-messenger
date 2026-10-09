package rcs

import (
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"

	"github.com/zagnu-labs/rcs-messenger/internal/api/httputil"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsphone"
)

const maxHARBytes = 64 << 20

type Pairer interface {
	StartQR() error
	QR() (string, error)
	Status() rcsphone.PairStatus
	Cancel()
	PairWithCookies(ctx context.Context, har []byte) (string, error)
	RefreshWithCookies(ctx context.Context, har []byte) error
}

func (e *Endpoint) PairPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(pairPage))
}

func (e *Endpoint) PairStart(c *gin.Context) {
	if e.pairing == nil {
		httputil.BadRequest(c, "pairing is not available")
		return
	}
	if err := e.pairing.StartQR(); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	httputil.OK(c, "pairing started", e.pairing.Status())
}

func (e *Endpoint) PairQR(c *gin.Context) {
	if e.pairing == nil {
		httputil.BadRequest(c, "pairing is not available")
		return
	}
	code, err := e.pairing.QR()
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	png, err := qrcode.Encode(code, qrcode.Medium, 512)
	if err != nil {
		httputil.InternalServerError(c, "could not render the QR code")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/png", png)
}

func (e *Endpoint) PairStatus(c *gin.Context) {
	if e.pairing == nil {
		httputil.OK(c, "pairing is not available", rcsphone.PairStatus{})
		return
	}
	httputil.OK(c, "ok", e.pairing.Status())
}

func (e *Endpoint) PairCancel(c *gin.Context) {
	if e.pairing == nil {
		httputil.BadRequest(c, "pairing is not available")
		return
	}
	e.pairing.Cancel()
	httputil.OK(c, "cancelled", nil)
}

func (e *Endpoint) PairCookies(c *gin.Context) {
	har, ok := readHAR(c)
	if !ok {
		return
	}
	emoji, err := e.pairing.PairWithCookies(c.Request.Context(), har)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	httputil.OK(c, "tap this emoji on the phone", gin.H{"emoji": emoji})
}

func (e *Endpoint) PairRefresh(c *gin.Context) {
	har, ok := readHAR(c)
	if !ok {
		return
	}
	if err := e.pairing.RefreshWithCookies(c.Request.Context(), har); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	httputil.OK(c, "session refreshed", nil)
}

func readHAR(c *gin.Context) ([]byte, bool) {
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxHARBytes)
	raw, err := io.ReadAll(body)
	if err != nil {
		httputil.BadRequest(c, "could not read the uploaded HAR")
		return nil, false
	}
	if len(raw) == 0 {
		httputil.BadRequest(c, "the request body is empty, POST the .har file as the body")
		return nil, false
	}
	return raw, true
}

const pairPage = `<!doctype html>
<meta charset="utf-8">
<title>Pair a phone</title>
<style>
  body { font-family: system-ui, sans-serif; background: #111; color: #eee;
         display: grid; place-items: center; min-height: 100vh; margin: 0; }
  .card { text-align: center; max-width: 36rem; padding: 1rem; }
  img { width: 320px; height: 320px; background: #fff; padding: 12px; border-radius: 12px; }
  p { color: #aaa; line-height: 1.5; }
  button { font: inherit; padding: .6rem 1.2rem; border-radius: 8px; border: 0;
           background: #0ad85f; color: #000; font-weight: 600; cursor: pointer; }
  button[disabled] { opacity: .5; cursor: default; }
  .ok { color: #0ad85f; font-weight: 600; }
  .bad { color: #ff6b6b; font-weight: 600; }
  .muted { color: #777; font-size: .9rem; }
</style>
<div class="card">
  <h2>Pair a phone</h2>
  <div id="idle">
    <p>On the phone: Google Messages &rsaquo; profile menu &rsaquo; Device pairing &rsaquo; QR code scanner.</p>
    <button id="go" onclick="start()">Start pairing</button>
    <p id="hint" class="muted"></p>
  </div>
  <div id="live" style="display:none">
    <img id="qr" alt="pairing QR code">
    <p>Scan it from the phone. The code refreshes every 30 seconds.</p>
  </div>
  <p id="status"></p>
</div>
<script>
  const el = id => document.getElementById(id);
  const say = (cls, text) => { el('status').className = cls; el('status').textContent = text; };

  async function start() {
    el('go').disabled = true;
    el('hint').textContent = 'Contacting Google, this takes a few seconds...';
    say('', '');
    try {
      const r = await fetch('pair/start', { method: 'POST' });
      const j = await r.json();
      if (!j.success) {
        el('go').disabled = false;
        el('hint').textContent = '';
        say('bad', j.error || 'Could not start pairing.');
        return;
      }
      el('idle').style.display = 'none';
      el('live').style.display = '';
      refresh();
    } catch (e) {
      el('go').disabled = false;
      el('hint').textContent = '';
      say('bad', 'Could not reach the server: ' + e.message);
    }
  }

  function refresh() { el('qr').src = 'pair/qr?t=' + Date.now(); }
  el('qr').onerror = () => say('bad', 'The QR image failed to load, try Start pairing again.');

  setInterval(() => { if (el('live').style.display !== 'none') refresh(); }, 25000);

  setInterval(async () => {
    try {
      const s = (await (await fetch('pair/status')).json()).data;
      if (!s) return;
      if (s.paired) {
        el('live').style.display = 'none';
        say('ok', 'Paired with ' + (s.phone || 'the phone') + '. You can close this page.');
      } else if (s.done) {
        el('live').style.display = 'none';
        say('bad', s.error || 'Pairing did not complete.');
      }
    } catch (e) {}
  }, 2000);
</script>
`
