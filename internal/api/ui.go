package api

import "net/http"

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>grammar-server</title>
<style>
:root{--bg:#14141f;--fg:#e6e6ef;--muted:#8b8ba3;--accent:#7c3aed;--card:#1e1e2e;--border:#2c2c44;--err:#f87171;--spell:#f59e0b;--style:#818cf8;--ok:#34d399}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;background:var(--bg);color:var(--fg);min-height:100vh;display:flex;justify-content:center;padding:2rem 1rem}
.app{width:100%;max-width:860px;display:flex;flex-direction:column;gap:1rem}
h1{font-size:1.3rem;font-weight:650;display:flex;align-items:center;gap:.55rem}
h1 em{font-style:normal;background:var(--accent);color:#fff;font-size:.62rem;padding:.18rem .5rem;border-radius:4px;text-transform:uppercase;letter-spacing:.06em}
textarea{width:100%;min-height:200px;padding:.85rem 1rem;background:var(--card);color:var(--fg);border:1.5px solid var(--border);border-radius:8px;font:inherit;resize:vertical;line-height:1.65}
textarea:focus{outline:none;border-color:var(--accent)}
.controls{display:flex;gap:.7rem;align-items:center}
select,button{padding:.5rem .95rem;border-radius:6px;font:inherit;border:1.5px solid var(--border);background:var(--card);color:var(--fg);cursor:pointer}
button{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:500}
button:disabled{opacity:.5}
.ghost{background:var(--card);border-color:var(--border);color:var(--fg)}
.status{font-size:.78rem;color:var(--muted);margin-left:auto}
.results{display:flex;flex-direction:column;gap:.6rem}
.match{background:var(--card);border:1px solid var(--border);border-left:3px solid var(--style);border-radius:6px;padding:.8rem .95rem}
.match.grammar{border-left-color:var(--err)}
.match.spelling{border-left-color:var(--spell)}
.match .msg{font-weight:600;font-size:.92rem;margin-bottom:.25rem}
.match .rule{font-size:.72rem;color:var(--muted)}
.reps{margin-top:.5rem;display:flex;flex-wrap:wrap;gap:.35rem}
.rep{background:var(--border);color:var(--fg);font-size:.8rem;padding:.28rem .65rem;border-radius:5px;border:1px solid transparent;cursor:pointer}
.rep:hover{background:var(--accent);border-color:var(--accent);color:#fff}
.snippet{line-height:1.9;white-space:pre-wrap;font-size:.95rem;background:var(--card);border:1px solid var(--border);border-radius:6px;padding:.85rem 1rem}
.snippet mark{border-radius:3px;padding:0 2px}
mark.t-grammar{background:color-mix(in srgb,var(--err) 75%,#000);color:#000}
mark.t-spelling{background:color-mix(in srgb,var(--spell) 80%,#000);color:#000}
mark.t-style,mark.t-typography{background:color-mix(in srgb,var(--style) 80%,#000);color:#000}
.ok{color:var(--ok);font-weight:500}
</style>
</head>
<body>
<div class="app">
<h1>grammar-server <em>offline</em></h1>
<textarea id="text" placeholder="Type or paste text — checks live as you write…" autofocus>This sentence have an error. teh quick brown fox</textarea>
<div class="controls">
<select id="lang"><option value="en-US">English (US)</option><option value="en-GB">English (UK)</option><option value="en-CA">English (CA)</option><option value="en-AU">English (AU)</option><option value="en-IN">English (IN)</option></select>
<button class="ghost" onclick="applyAll()">Fix all</button>
<span class="status" id="status"></span>
</div>
<div class="results" id="results"></div>
</div>
<script>
function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
var currentMatches=[], lastText='';

var ta=document.getElementById('text'), statusEl=document.getElementById('status'), resEl=document.getElementById('results'), timer=null;

function words(t){var w=t.trim().split(/\s+/).filter(Boolean);return w.length}

async function run(){
  var text=ta.value, lang=document.getElementById('lang').value;
  lastText=text;
  if(!text.trim()){resEl.innerHTML='';statusEl.textContent='';return}
  statusEl.textContent='checking…';
  try{
    var r=await fetch('/v2/check',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({text:text,language:lang})});
    var d=await r.json();
    if(ta.value!==text)return; // text changed while checking
    currentMatches=d.matches;
    render(text,d.matches);
    statusEl.textContent=words(text)+' words · '+d.matches.length+' issue'+(d.matches.length!==1?'s':'');
  }catch(e){statusEl.textContent='Error: '+e.message}
}

function render(text,matches){
  if(!matches.length){resEl.innerHTML='<p class="ok">'+String.fromCharCode(10003)+' No issues found</p>';return}
  var parts=[],pos=0;
  var sorted=matches.slice().sort(function(a,b){return a.offset-b.offset});
  var lastEnd=-1;
  for(var i=0;i<sorted.length;i++){
    var m=sorted[i],off=m.offset,len=m.length;
    if(off<lastEnd)continue; // skip overlaps
    if(off>pos)parts.push(esc(text.slice(pos,off)));
    parts.push('<mark class="t-'+m.type.typeName+'" title="'+esc(m.message)+'">'+esc(text.slice(off,off+len))+'</mark>');
    pos=off+len; lastEnd=pos;
  }
  if(pos<text.length)parts.push(esc(text.slice(pos)));
  var html='<div class="snippet">'+parts.join('')+'</div>';
  for(var i=0;i<matches.length;i++){
    var m=matches[i];
    html+='<div class="match '+m.type.typeName+'"><div class="msg">'+esc(m.message)+'</div>';
    html+='<div class="rule">'+esc(m.rule.id)+' · offset '+m.offset+', len '+m.length+'</div>';
    if(m.replacements.length){
      html+='<div class="reps">';
      for(var j=0;j<m.replacements.length;j++){
        html+='<button class="rep" data-mi="'+i+'" data-ri="'+j+'">'+esc(m.replacements[j].value)+'</button>';
      }
      html+='</div>';
    }
    html+='</div>';
  }
  resEl.innerHTML=html;
}

function applySuggestion(mi,ri){
  var m=currentMatches[mi]; if(!m)return;
  var rep=m.replacements[ri].value, t=ta.value;
  if(m.offset+m.length>t.length)return;
  ta.value=t.slice(0,m.offset)+rep+t.slice(m.offset+m.length);
  run();
}

function applyAll(){
  // apply first suggestion of each match, from the END so earlier offsets stay valid
  var t=ta.value, ms=currentMatches.slice().sort(function(a,b){return b.offset-a.offset});
  var changed=false;
  for(var i=0;i<ms.length;i++){
    var m=ms[i]; if(!m.replacements.length)continue;
    var rep=m.replacements[0].value;
    if(m.offset+m.length>t.length)continue;
    t=t.slice(0,m.offset)+rep+t.slice(m.offset+m.length);
    changed=true;
  }
  if(changed){ta.value=t; run()}
}

resEl.addEventListener('click',function(e){
  var b=e.target.closest('.rep'); if(!b)return;
  applySuggestion(+b.getAttribute('data-mi'),+b.getAttribute('data-ri'));
});

ta.addEventListener('input',function(){
  clearTimeout(timer);
  timer=setTimeout(run,500);
});

run(); // initial check
</script>
</body>
</html>`

func (s *Server) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(uiHTML))
}