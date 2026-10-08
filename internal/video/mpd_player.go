package video

import (
	"net/http"
)

func (h *Handler) MPDPlayerScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(mpdPlayerJS))
}
func (h *Handler) MPDPreviewPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Staff recording preview</title><body><h1>Staff preview</h1><p id="mpd-status">Checking access...</p><video id="player" controls playsinline crossorigin="anonymous" style="width:100%;max-height:80vh"></video><script src="/api/mpd/player.js" defer></script></body></html>`))
}

const mpdPlayerJS = `(function(){
'use strict';
var player=document.getElementById('player');if(!player)return;
var preview=location.pathname==='/mpd-preview', proof='', session=null, sent=false, observed=null, renewing=null, activeToken='', timer=null;
function managed(){return preview||player.dataset.mpdManaged==='true';}
function status(text){var el=document.getElementById('mpd-status');if(el)el.textContent=text;}
function stop(){player.pause();player.removeAttribute('src');player.load();status('Access ended. Sign in again and open a new preview.');if(timer)clearInterval(timer);}
function share(){if(player.dataset.shareToken)return player.dataset.shareToken;var parts=location.pathname.split('/');return parts.length===3?parts[2]:'';}
async function request(path,body){var headers={};if(proof)headers.Authorization='Bearer '+proof;if(body!==undefined)headers['Content-Type']='application/json';var response=await fetch(path,{method:body===undefined?'GET':'POST',headers:headers,body:body===undefined?undefined:JSON.stringify(body),credentials:'same-origin',cache:'no-store'});if(!response.ok)throw new Error('Access unavailable');if(response.status===204)return {};return response.json();}
function endpoint(suffix){return preview?'/api/mpd/preview/'+suffix:'/api/watch/'+encodeURIComponent(share())+'/'+(suffix==='session'?'playback-session':suffix);}
async function newSession(){if(!managed())return;var token=share();activeToken=token;session=null;sent=false;observed=null;var result=await request(endpoint('session'),{});if(!managed()||!preview&&share()!==token)return;session=result;}
async function renew(){if(!managed())return;if(renewing)return renewing;var token=share();renewing=(async function(){var media=await request(preview?'/api/mpd/preview/media':endpoint('renew'),preview?{}:undefined);if(!managed()||share()!==token)return;var position=player.currentTime, paused=player.paused;await new Promise(function(resolve,reject){player.addEventListener('loadedmetadata',function(){player.currentTime=position;resolve();},{once:true});player.addEventListener('error',reject,{once:true});player.src=media.videoUrl;player.load();});if(!paused)await player.play();if(session&&media.mediaVersion&&session.mediaVersion!==media.mediaVersion){await newSession();}if(media.transcriptUrl){var track=player.querySelector('track');if(!track){track=document.createElement('track');track.kind='subtitles';track.srclang='en';track.label='Subtitles';player.appendChild(track);}track.src=media.transcriptUrl;}return media;})().catch(function(){if(managed()&&share()===token)stop();}).finally(function(){renewing=null;});return renewing;}
async function start(){
 if(preview){var values=new URLSearchParams(location.hash.slice(1));history.replaceState(null,'',location.pathname);var body={handoff:values.get('handoff'),nonce:values.get('nonce'),parentOrigin:values.get('parentOrigin')};
  if(window.parent!==window && !body.handoff){body=await new Promise(function(resolve){var nonce=values.get('nonce'),origin=values.get('parentOrigin');function receive(event){if(event.origin!==origin||event.source!==window.parent||!event.data||event.data.nonce!==nonce||event.data.type!=='mpd-preview')return;window.removeEventListener('message',receive);resolve({handoff:event.data.handoff,nonce:nonce,parentOrigin:origin});}window.addEventListener('message',receive);window.parent.postMessage({type:'mpd-preview-ready',nonce:nonce},origin);});}
  var redeemed=await request('/api/mpd/preview/redeem',body);proof=redeemed.previewToken;await renew();status('Private staff preview. This does not mark a patient report watched.');
 }
 await activate();
}
async function activate(){if(timer)clearInterval(timer);timer=null;session=null;sent=false;observed=null;activeToken=share();var token=activeToken;if(!managed())return;try{await newSession();}catch(e){if(preview)throw e;}if(managed()&&share()===token)timer=setInterval(function(){renew();},240000);}
player.addEventListener('mpd-recording-change',function(){activate().catch(function(){if(preview)stop();});});
player.addEventListener('playing',function(){observed={time:player.currentTime,wall:performance.now()};});
player.addEventListener('seeking',function(){observed=null;});
player.addEventListener('pause',function(){observed=null;});
player.addEventListener('error',function(){if(session&&!renewing)renew();});
player.addEventListener('timeupdate',async function(){
 if(!preview&&activeToken!==share()){await activate();return;}
 if(!managed())return;
 if(!session||sent||player.paused||player.seeking||renewing)return;
 var now=performance.now();if(!observed){observed={time:player.currentTime,wall:now};return;}var elapsed=now-observed.wall,delta=player.currentTime-observed.time;
 if(elapsed<300||delta<0.2)return;
 if(delta>elapsed/1000*2.1){observed=null;return;}
 try{if(Date.parse(session.expiresAt)<=Date.now()){await newSession();return;}await request(endpoint('playback-start'),{playbackSessionId:session.playbackSessionId,previousTime:observed.time,currentTime:player.currentTime,elapsedMilliseconds:Math.round(elapsed),playing:true,seeking:false});sent=true;}catch(e){observed=null;}
});
start().catch(function(){if(preview)stop();});
})();`
