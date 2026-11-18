(function(){
'use strict';
var player=document.getElementById('player');if(!player)return;
var preview=location.pathname==='/mpd-preview', proof='', session=null, sent=false, observed=null, renewing=null, activeToken='', timer=null;
function managed(){return preview||player.dataset.mpdManaged==='true';}
function status(text){var el=document.getElementById('mpd-status');if(el)el.textContent=text;}
var ENDED='Access ended. Sign in again and open a new preview.', UNAVAILABLE='The preview isn\'t available right now. Try again.', described=null;
// SendRec's own video page frames the preview from the same origin; Staff OS frames it, or opens it in a tab, from its own.
function ownFrame(){try{return window.parent!==window&&window.parent.location.origin===location.origin;}catch(e){return false;}}
function closed(){return ownFrame()?'This preview has closed. Reload the page to open the preview again.':'This preview has closed. Open it again from Staff OS.';}
// The server says why it refused: 410 the handoff or preview is spent, expired, or for a deleted or changed
// recording; 403 the staff session ended; anything else may pass.
function refusal(e){return e&&e.status===410?closed():e&&e.status===403?ENDED:UNAVAILABLE;}
// A preview request that may pass (no answer, 409 not ready, 5xx) is tried once more before the page gives up.
var RETRY_MS=2000;
function passing(e){return !e||!e.status||e.status===409||e.status>=500;}
function again(attempt){return attempt().catch(function(e){if(!passing(e))throw e;return new Promise(function(resolve){setTimeout(resolve,RETRY_MS);}).then(attempt);});}
function el(id){return document.getElementById(id);}
function forget(){described=null;var meta=el('mpd-meta');if(!meta)return;meta.classList.remove('shown','full');el('mpd-title').textContent='Staff preview';el('mpd-detail').textContent='';var more=el('mpd-more');more.hidden=true;more.setAttribute('aria-expanded','false');more.textContent='Show full title';}
// The server writes the date (in the clinic's zone) and the link state, so the header reads the same in any browser.
function detail(){if(!described)return;el('mpd-detail').textContent=[described.recorded,described.linkState].filter(function(part){return typeof part==='string'&&part;}).join(' \u00b7 ');}
// Chromium's line-clamp leaves scrollHeight equal to the clamped height, so the title is measured both ways. The
// toggle shows only while the whole title is longer than two lines; a title that fits after widening drops back.
function more(full){var button=el('mpd-more');el('mpd-meta').classList.toggle('full',full);button.setAttribute('aria-expanded',String(full));button.textContent=full?'Show less':'Show full title';}
function fold(){var meta=el('mpd-meta'),title=el('mpd-title');if(!described||!meta.classList.contains('shown'))return;var full=meta.classList.contains('full');meta.classList.remove('full');var clamped=title.getBoundingClientRect().height;meta.classList.add('full');var whole=title.scrollHeight;meta.classList.toggle('full',full);var fits=whole<=clamped+1;el('mpd-more').hidden=fits;if(fits&&full)more(false);}
var folding=0;function refold(){if(!folding)folding=requestAnimationFrame(function(){folding=0;fold();});}
function describe(v){var meta=el('mpd-meta');if(!meta||!v||typeof v.title!=='string'||!v.title)return;described={recorded:v.recorded,linkState:v.linkState};el('mpd-title').textContent=v.title;detail();if(window.parent===window){meta.classList.add('shown');fold();}}
function relink(state){if(!described||typeof state!=='string')return;described.linkState=state;detail();}
function stop(message){player.pause();player.removeAttribute('src');player.load();forget();status(message||ENDED);if(timer)clearInterval(timer);}
if(preview){el('mpd-more').addEventListener('click',function(){more(!el('mpd-meta').classList.contains('full'));fold();});addEventListener('resize',refold);}
function share(){if(player.dataset.shareToken)return player.dataset.shareToken;var parts=location.pathname.split('/');return parts.length===3?parts[2]:'';}
async function request(path,body){var headers={};if(proof)headers.Authorization='Bearer '+proof;if(body!==undefined)headers['Content-Type']='application/json';var response=await fetch(path,{method:body===undefined?'GET':'POST',headers:headers,body:body===undefined?undefined:JSON.stringify(body),credentials:'same-origin',cache:'no-store',keepalive:body!==undefined});if(!response.ok){var failure=new Error('Access unavailable');failure.status=response.status;throw failure;}if(response.status===204)return {};return response.json();}
function endpoint(suffix){return preview?'/api/mpd/preview/'+suffix:'/api/watch/'+encodeURIComponent(share())+'/'+(suffix==='session'?'playback-session':suffix);}
async function newSession(){if(!managed())return;var token=share();activeToken=token;session=null;sent=false;observed=null;var result=await request(endpoint('session'),{});if(!managed()||!preview&&share()!==token)return;session=result;}
async function renew(){if(!managed())return;if(renewing)return renewing;var token=share();var attempt=async function(){var media=await request(preview?'/api/mpd/preview/media':endpoint('renew'),preview?{}:undefined);if(!managed()||share()!==token)return;if(preview)relink(media.linkState);var position=player.currentTime, paused=player.paused;await new Promise(function(resolve,reject){player.addEventListener('loadedmetadata',function(){player.currentTime=position;resolve();},{once:true});player.addEventListener('error',reject,{once:true});player.src=media.videoUrl;player.load();});if(!paused)await player.play();if(session&&media.mediaVersion&&session.mediaVersion!==media.mediaVersion){await newSession();}if(media.transcriptUrl){var track=player.querySelector('track');if(!track){track=document.createElement('track');track.kind='subtitles';track.srclang='en';track.label='Subtitles';player.appendChild(track);}track.src=media.transcriptUrl;}return media;};renewing=(preview?again(attempt):attempt()).catch(function(e){if(managed()&&share()===token)stop(preview?refusal(e):undefined);}).finally(function(){renewing=null;});return renewing;}
async function start(){
 if(preview){var hash=location.hash.slice(1),values=new URLSearchParams(hash);history.replaceState(null,'',location.pathname);var body={handoff:values.get('handoff'),nonce:values.get('nonce'),parentOrigin:values.get('parentOrigin')};
  if(window.parent!==window && !body.handoff && body.nonce && body.parentOrigin){body=await new Promise(function(resolve){var nonce=values.get('nonce'),origin=values.get('parentOrigin');function receive(event){if(event.origin!==origin||event.source!==window.parent||!event.data||event.data.nonce!==nonce||event.data.type!=='mpd-preview')return;window.removeEventListener('message',receive);resolve({handoff:event.data.handoff,nonce:nonce,parentOrigin:origin});}window.addEventListener('message',receive);window.parent.postMessage({type:'mpd-preview-ready',nonce:nonce},origin);});}
  // The handoff is good for one use, and the page drops it from the address as it starts, so a refresh or a
  // reopened tab has none left: staff are still signed in and need only open the preview again.
  if(!body.handoff){stop(closed());return;}
  // A refused redeem leaves the handoff unspent unless it was already spent, so after a passing fault the
  // address gets it back and a reload tries again.
  var redeemed;try{redeemed=await again(function(){return request('/api/mpd/preview/redeem',body);});}catch(e){if(passing(e)&&hash)history.replaceState(null,'',location.pathname+'#'+hash);stop(refusal(e));return;}proof=redeemed.previewToken;await renew();if(player.getAttribute('src')){describe(redeemed);status('Private staff preview. This does not mark a patient report watched.');}
 }
 await activate();
}
async function activate(){if(timer)clearInterval(timer);timer=null;session=null;sent=false;observed=null;activeToken=share();var token=activeToken;if(!managed())return;try{await newSession();}catch(e){if(preview)throw e;}if(managed()&&share()===token)timer=setInterval(function(){renew();},240000);}
player.addEventListener('mpd-recording-change',function(){activate().catch(function(e){if(preview)stop(refusal(e));});});
player.addEventListener('playing',function(){flush();observed={time:player.currentTime,wall:performance.now()};});
player.addEventListener('seeking',function(){flush();observed=null;});
player.addEventListener('pause',function(){flush();observed=null;});
player.addEventListener('error',function(){if(session&&!renewing)renew();});
player.addEventListener('timeupdate',async function(){
 if(!preview&&activeToken!==share()){await activate();return;}
 if(!managed())return;
 if(!session||sent||player.paused||player.seeking||renewing)return;
 var now=performance.now();if(!observed){observed={time:player.currentTime,wall:now};return;}var elapsed=now-observed.wall,delta=player.currentTime-observed.time;
 if(elapsed<300||delta<0.2)return;
 if(delta>elapsed/1000*2.1){observed=null;return;}
 try{if(Date.parse(session.expiresAt)<=Date.now()){await newSession();return;}await request(endpoint('playback-start'),{playbackSessionId:session.playbackSessionId,previousTime:observed.time,currentTime:player.currentTime,elapsedMilliseconds:Math.round(elapsed),playing:true,seeking:false});sent=true;seen={time:player.currentTime,wall:performance.now()};}catch(e){observed=null;}
});
// After the start, a patient's link reports the stretch it has played every few seconds, and the stretch since the last
// report when playback pauses or seeks, so MPD can tell when a session has played most of the video. A preview never
// counts, so it reports nothing more.
var seen=null, done=false, refused=0;
function report(from,to){
 if(preview||done||!managed()||!session||!sent||!from||!to)return;
 var elapsed=to.wall-from.wall,delta=to.time-from.time;
 if(elapsed<250||elapsed>10000||delta<0.2||delta>elapsed/1000*2.1)return;
 request(endpoint('playback-progress'),{playbackSessionId:session.playbackSessionId,previousTime:from.time,currentTime:to.time,elapsedMilliseconds:Math.round(elapsed),playing:true,seeking:false})
  .then(function(answer){refused=0;if(answer&&answer.expiresAt)session.expiresAt=answer.expiresAt;if(answer&&answer.completed)done=true;})
  // Two refusals in a row, or a session past its time, mean the server closed it: open a new one, which starts again.
  .catch(function(){refused++;if(session&&(refused>=2||Date.parse(session.expiresAt)<=Date.now())){refused=0;done=false;newSession().catch(function(){});}});
}
function flush(){if(observed&&seen&&seen.time>observed.time)report(observed,seen);seen=null;}
// Closing or hiding the page reports the stretch since the last report; keepalive lets it outlive the page.
addEventListener('pagehide',function(){flush();observed=null;});
document.addEventListener('visibilitychange',function(){if(document.visibilityState==='hidden'){flush();observed=null;}});
player.addEventListener('timeupdate',function(){
 if(preview||!managed()||!session||!sent||player.paused||player.seeking||renewing)return;
 var now={time:player.currentTime,wall:performance.now()};
 if(!observed||!seen){if(!observed)observed=now;seen=now;return;}
 if(now.time<seen.time||now.time-seen.time>(now.wall-seen.wall)/1000*2.1+0.5){flush();observed=now;seen=now;return;}
 seen=now;
 if(now.wall-observed.wall>=5000){report(observed,now);observed=now;}
});
start().catch(function(e){if(preview)stop(refusal(e));});
})();
