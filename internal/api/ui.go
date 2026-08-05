package api

import "net/http"

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>grammar-server</title>
<style>
:root{--bg:#1a1a2e;--fg:#e0e0e0;--accent:#7c3aed;--card:#16213e;--border:#2a2a4a;--err:#ef4444;--warn:#f59e0b;--ok:#10b981}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;background:var(--bg);color:var(--fg);min-height:100vh;display:flex;justify-content:center;padding:2rem 1rem}
.app{width:100%;max-width:820px;display:flex;flex-direction:column;gap:1.25rem}
h1{font-size:1.35rem;font-weight:600;display:flex;align-items:center;gap:.5rem}
h1 em{font-style:normal;background:var(--accent);color:#fff;font-size:.65rem;padding:.15rem .5rem;border-radius:4px;text-transform:uppercase}
textarea{width:100%;min-height:180px;padding:.85rem 1rem;background:var(--card);color:var(--fg);border:1.5px solid var(--border);border-radius:8px;font:inherit;resize:vertical;line-height:1.6}
textarea:focus{outline:none;border-color:var(--accent)}
.controls{display:flex;gap:.75rem;align-items:center}
select,button{padding:.55rem 1rem;border-radius:6px;font:inherit;border:1.5px solid var(--border);background:var(--card);color:var(--fg);cursor:pointer}
button{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:500;min-width:100px}
button:disabled{opacity:.5}
.results{display:flex;flex-direction:column;gap:.6rem}
.match{background:var(--card);border:1px solid var(--border);border-left:3px solid var(--warn);border-radius:6px;padding:.85rem 1rem}
.match.grammar{border-left-color:var(--err)}
.match.spelling,.typography{border-left-color:var(--accent)}
.match .msg{font-weight:600;margin-bottom:.3rem}
.match .rule{font-size:.75rem;color:#888}
.match .reps{margin-top:.5rem;display:flex;flex-wrap:wrap;gap:.35rem}
.rep{background:var(--border);color:var(--fg);font-size:.8rem;padding:.25rem .6rem;border-radius:4px}
.status{font-size:.8rem;color:#777;margin-left:auto}
.snippet{line-height:1.8;white-space:pre-wrap;font-size:.95rem;background:var(--card);border:1px solid var(--border);border-radius:6px;padding:.85rem 1rem;margin-bottom:.5rem}
.snippet mark{background:var(--warn);color:#000;border-radius:3px;padding:0 2px}
.ok{color:var(--ok);font-weight:500}
</style>
</head>
<body>
<div class="app">
<h1>grammar-server <em>offline</em></h1>
<textarea id="text" placeholder="Paste text to check…" autofocus>This sentence have an error. teh quick brown fox</textarea>
<div class="controls">
<select id="lang"><option value="en-US">English (US)</option><option value="en-GB">English (UK)</option><option value="en-CA">English (CA)</option><option value="en-AU">English (AU)</option><option value="en-IN">English (IN)</option></select>
<button id="check" onclick="run()">Check</button>
<span class="status" id="status"></span>
</div>
<div class="results" id="results"></div>
</div>
<script>
function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}
async function run(){
  var text=document.getElementById('text').value, lang=document.getElementById('lang').value;
  if(!text.trim())return;
  var st=document.getElementById('status'), res=document.getElementById('results'), btn=document.getElementById('check');
  st.textContent='checking…'; btn.disabled=true;
  try{
    var r=await fetch('/v2/check',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({text:text,language:lang})}), d=await r.json();
    render(text,d.matches);
    st.textContent=d.matches.length+' issue'+(d.matches.length!=1?'s':'');
    st.className='status';
  }catch(e){st.textContent='Error: '+e.message; st.className='status';}
  btn.disabled=false;
}
function render(text,matches){
  var out=document.getElementById('results');
  if(!matches.length){out.innerHTML='<p class=ok>'+String.fromCharCode(0x2713)+' No issues found</p>';return}
  var parts=[],pos=0;
  var sorted=matches.slice().sort(function(a,b){return a.offset-b.offset||b.length-a.length});
  for(var i=0;i<sorted.length;i++){
    var m=sorted[i], off=m.offset, len=m.length;
    // skip if overlapping an already-rendered span
    var skip=false;
    for(var j=0;j<i;j++){var p=sorted[j];if(off>=p.offset&&off<p.offset+p.length)skip=true}
    if(skip)continue;
    if(off>pos)parts.push(esc(text.slice(pos,off)));
    parts.push('<mark title=' + JSON.stringify(m.message) + '>' + esc(text.slice(off,off+len)) + '</mark>');
    pos=off+len;
  }
  if(pos<text.length)parts.push(esc(text.slice(pos)));
  var html='<div class=snippet>'+parts.join('')+'</div>';
  for(var i=0;i<matches.length;i++){
    var m=matches[i];
    html+='<div class="match '+m.type.typeName+'"><div class=msg>'+esc(m.message)+'</div>';
    html+='<div class=rule>rule: '+esc(m.rule.id)+' &middot; off '+m.offset+', len '+m.length+'</div>';
    if(m.replacements.length){
      html+='<div class=reps>';
      for(var j=0;j<m.replacements.length;j++)html+='<span class=rep>'+esc(m.replacements[j].value)+'</span>';
      html+='</div>';
    }
    html+='</div>';
  }
  out.innerHTML=html;
}
</script>
</body>
</html>`

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(uiHTML))
}