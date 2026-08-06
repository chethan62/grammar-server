package api

import "net/http"

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>grammar-server</title>
<style>
:root{
  --bg:#0c0c14; --surface:#131323; --card:#18182d; --border:#252540;
  --fg:#e4e4f0; --muted:#78789a; --accent:#7c3aed; --accent-glow:rgba(124,58,237,.35);
  --err:#f77070; --spell:#f0a040; --style:#8899f0; --ok:#44cc88;
  --radius:9px; --font:system-ui,-apple-system,sans-serif;
}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:var(--font);background:var(--bg);color:var(--fg);height:100vh;overflow:hidden}
.app{height:100vh;display:flex;flex-direction:column}
header{display:flex;align-items:center;justify-content:space-between;padding:.75rem 1.25rem;border-bottom:1px solid var(--border);background:var(--surface);flex-shrink:0}
header h1{font-size:1.05rem;font-weight:650;display:flex;align-items:center;gap:.5rem}
header h1 .dot{width:8px;height:8px;border-radius:50%;background:var(--ok)}
header .ver{font-size:.7rem;color:var(--muted);text-transform:uppercase;letter-spacing:.05em}
main{display:flex;flex:1;overflow:hidden}
.pane{flex:1;display:flex;flex-direction:column;overflow:hidden}
.pane+.pane{border-left:1px solid var(--border)}
.pane-head{padding:.65rem 1.25rem;font-size:.78rem;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid var(--border);display:flex;align-items:center;justify-content:space-between;flex-shrink:0}
.pane-body{flex:1;overflow-y:auto;padding:1rem 1.25rem;display:flex;flex-direction:column;gap:.8rem}
textarea{flex:1;min-height:0;background:transparent;color:var(--fg);border:none;font:inherit;font-size:.95rem;line-height:1.7;resize:none;outline:none;padding:0}
textarea::placeholder{color:var(--muted)}
.toolbar{display:flex;gap:.5rem;align-items:center;padding:.5rem 0}
select,button{padding:.45rem .85rem;border-radius:6px;font:inherit;font-size:.82rem;border:1px solid var(--border);background:var(--card);color:var(--fg);cursor:pointer}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:550}
button.ghost{background:transparent;border-color:var(--border)}
button:hover{opacity:.9}
.stats{font-size:.72rem;color:var(--muted);margin-left:auto}
.snippet{line-height:1.9;white-space:pre-wrap;font-size:.95rem;border-radius:6px;padding:.75rem 1rem;background:var(--card)}
.snippet mark{border-radius:3px;padding:0 2px}
mark.t-grammar{background:var(--err);color:#000}
mark.t-spelling{background:var(--spell);color:#000}
mark.t-style,.t-typography{background:color-mix(in srgb,var(--style) 85%,#000);color:#000}
.match{background:var(--card);border:1px solid var(--border);border-left:3px solid var(--style);border-radius:6px;padding:.75rem .9rem}
.match.grammar{border-left-color:var(--err)}
.match.spelling{border-left-color:var(--spell)}
.match .msg{font-weight:600;font-size:.88rem;margin-bottom:.2rem}
.match .rule{font-size:.7rem;color:var(--muted)}
.match .reps{margin-top:.45rem;display:flex;flex-wrap:wrap;gap:.3rem}
.rep{background:var(--border);color:var(--fg);font-size:.78rem;padding:.25rem .6rem;border-radius:5px;border:1px solid transparent;cursor:pointer}
.rep:hover{background:var(--accent);border-color:var(--accent);color:#fff}
.empty{color:var(--muted);font-size:.88rem;font-style:italic}
.ok{color:var(--ok);font-weight:500}
</style>
</head>
<body>
<div class="app">
<header>
  <h1><span class="dot"></span>Grammar Server<span style="color:var(--muted);font-weight:400;font-size:.82rem"> &middot; offline</span></h1>
  <span class="ver" id="version"></span>
</header>
<main>
<div class="pane">
  <div class="pane-head">Input <span class="stats" id="inputStats">0 ch &middot; 0 w</span></div>
  <div class="pane-body">
    <textarea id="text" placeholder="Write or paste text… live checking as you type" autofocus>This sentence have an error. teh quick brown fox</textarea>
    <div class="toolbar">
      <select id="lang"><option value="en-US">English (US)</option><option value="en-GB">English (UK)</option><option value="en-CA">English (CA)</option><option value="en-AU">English (AU)</option><option value="en-IN">English (IN)</option></select>
      <button class="primary" onclick="applyAll()">Fix all</button>
      <button class="ghost" onclick="copyText()">Copy text</button>
    </div>
  </div>
</div>
<div class="pane">
  <div class="pane-head">Issues <span class="stats" id="issueStats"></span></div>
  <div class="pane-body" id="results"><p class="empty">Checking…</p></div>
</div>
</main>
</div>
<script>
function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
var currentMatches=[],lastText='',timer=null;

var ta=document.getElementById('text'),resEl=document.getElementById('results'),
    langEl=document.getElementById('lang'),statsEl=document.getElementById('inputStats'),
    issueEl=document.getElementById('issueStats');

function words(t){return t.trim().split(/\s+/).filter(Boolean).length}

async function run(){
  var text=ta.value,lang=langEl.value;
  lastText=text; var wc=words(text);
  statsEl.textContent=text.length+' ch · '+wc+' w';
  if(!wc){resEl.innerHTML='<p class="empty">Start writing…</p>';issueEl.textContent='';return}
  issueEl.textContent='checking…';
  try{
    var r=await fetch('/v2/check',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({text:text,language:lang})});
    var d=await r.json();
    if(ta.value!==text)return;
    currentMatches=d.matches;
    render(text,d.matches);
    issueEl.textContent=d.matches.length+' issue'+(d.matches.length!==1?'s':'');
  }catch(e){issueEl.textContent='Error';resEl.innerHTML='<p class="empty">'+esc(e.message)+'</p>'}
}

function render(text,matches){
  if(!matches.length){resEl.innerHTML='<p class="ok">No issues found</p>';return}
  var parts=[],pos=0,lastEnd=-1;
  var sorted=matches.slice().sort(function(a,b){return a.offset-b.offset});
  for(var i=0;i<sorted.length;i++){
    var m=sorted[i],off=m.offset,len=m.length;
    if(off<lastEnd)continue;
    if(off>pos)parts.push(esc(text.slice(pos,off)));
    parts.push('<mark class="t-'+m.type.typeName+'" title="'+esc(m.message)+'">'+esc(text.slice(off,off+len))+'</mark>');
    pos=off+len;lastEnd=pos;
  }
  if(pos<text.length)parts.push(esc(text.slice(pos)));
  var html='<div class="snippet">'+parts.join('')+'</div>';
  for(var i=0;i<matches.length;i++){
    var m=matches[i];
    html+='<div class="match '+m.type.typeName+'"><div class="msg">'+esc(m.message)+'</div>';
    html+='<div class="rule">'+esc(m.rule.id)+' · offset '+m.offset+', len '+m.length+'</div>';
    if(m.replacements.length){
      html+='<div class="reps">';
      for(var j=0;j<m.replacements.length;j++)
        html+='<button class="rep" data-mi="'+i+'" data-ri="'+j+'">'+esc(m.replacements[j].value)+'</button>';
      html+='</div>';
    }
    html+='<button class="rep" data-rewrite="'+i+'" style="margin-top:.35rem;background:var(--accent);border-color:var(--accent);color:#fff">'+String.fromCharCode(0x2728)+' Reword sentence</button>';
    html+='</div>';
  }
  resEl.innerHTML=html;
}

function applySuggestion(mi,ri){
  var m=currentMatches[mi];if(!m)return;
  var rep=m.replacements[ri].value,t=ta.value;
  if(m.offset+m.length>t.length)return;
  ta.value=t.slice(0,m.offset)+rep+t.slice(m.offset+m.length);
  run();
}
function applyAll(){
  var t=ta.value,ms=currentMatches.slice().sort(function(a,b){return b.offset-a.offset});
  var changed=false;
  for(var i=0;i<ms.length;i++){
    var m=ms[i];if(!m.replacements.length)continue;
    var rep=m.replacements[0].value;
    if(m.offset+m.length>t.length)continue;
    t=t.slice(0,m.offset)+rep+t.slice(m.offset+m.length);
    changed=true;
  }
  if(changed){ta.value=t;run()}
}
function copyText(){
  ta.select(); document.execCommand('copy');
  var btn=document.querySelector('button.ghost');
  btn.textContent='Copied!';setTimeout(function(){btn.textContent='Copy text'},1500);
}

resEl.addEventListener('click',function(e){
  var b=e.target.closest('.rep');if(!b)return;
  if(b.hasAttribute('data-rewrite')){
    rewordSentence(+b.getAttribute('data-rewrite'));return;
  }
  applySuggestion(+b.getAttribute('data-mi'),+b.getAttribute('data-ri'));
});
ta.addEventListener('input',function(){
  statsEl.textContent=ta.value.length+' ch · '+words(ta.value)+' w';
  clearTimeout(timer);timer=setTimeout(run,400);
});

fetch('/status').then(function(r){return r.json()}).then(function(d){
  document.getElementById('version').textContent='v'+d.version;
}).catch(function(){});

async function rewordSentence(mi){
  var m=currentMatches[mi],t=ta.value;if(!m)return;
  var off=m.offset,s=off;
  while(s>0&&t[s-1]!='.'&&t[s-1]!='!'&&t[s-1]!='?'&&t[s-1]!='\n')s--;
  var e=off;
  while(e<t.length&&t[e]!='.'&&t[e]!='!'&&t[e]!='?'){if(t[e]=='\n'&&e>off)break;e++}
  if(t[e]=='.'||t[e]=='!'||t[e]=='?')e++;
  while(s<e&&(t[s]==' '||t[s]=='.'||t[s]=='!'||t[s]=='?'))s++;
  var sentence=t.slice(s,e);
  var btn=document.querySelector('[data-rewrite="'+mi+'"]');
  if(btn){btn.textContent='Rewriting…';btn.disabled=true}
  try{
    var r=await fetch('/v2/rephrase',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({text:t,offset:off})});
    var d=await r.json();
    if(d.rephrased){ta.value=t.slice(0,s)+d.rephrased+t.slice(e);run()}
  }catch(ex){}
  if(btn){btn.textContent=String.fromCharCode(0x2728)+' Reword sentence';btn.disabled=false}
}

run();
</script>
</body>
</html>`

func (s *Server) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(uiHTML))
}