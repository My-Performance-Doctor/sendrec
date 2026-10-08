/// <reference types="node" />
import { beforeEach, afterEach, it, expect, vi } from 'vitest';
import {readFileSync} from 'node:fs';
import {resolve} from 'node:path';
const source=readFileSync(resolve(process.cwd(),'../internal/video/mpd_player.go'),'utf8');
const script=source.slice(source.indexOf('const mpdPlayerJS=')>=0?source.indexOf('const mpdPlayerJS='):source.indexOf('const mpdPlayerJS =')).split('`')[1];
let player:HTMLVideoElement;
let paused=true;
let request:ReturnType<typeof vi.fn>;
async function flush(){for(let i=0;i<12;i++)await Promise.resolve();}
beforeEach(()=>{
 vi.useFakeTimers({toFake:['Date','performance','setInterval','clearInterval']});
 history.replaceState(null,'','/watch/synthetic');
 document.body.innerHTML='<video id="player"></video>';
 player=document.getElementById('player') as HTMLVideoElement;
 paused=true;
 Object.defineProperty(player,'paused',{get:()=>paused,configurable:true});
 Object.defineProperty(player,'seeking',{get:()=>false,configurable:true});
 vi.spyOn(player,'load').mockImplementation(()=>player.dispatchEvent(new Event('loadedmetadata')));
 vi.spyOn(player,'pause').mockImplementation(()=>{paused=true;});
 vi.spyOn(player,'play').mockImplementation(async()=>{paused=false;});
 request=vi.fn(async(path:string)=>new Response(JSON.stringify(path.endsWith('playback-session')?{playbackSessionId:'synthetic-session',expiresAt:new Date(Date.now()+300000).toISOString()}:{videoUrl:'https://storage.example.test/renewed'}),{status:200}));
 vi.stubGlobal('fetch',request);
 window.eval(script);
});
afterEach(()=>{vi.clearAllTimers();vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks();history.replaceState(null,'','/');});
it('page rendering, seek and a stalled player do not record playback',async()=>{
 await flush();expect(request).toHaveBeenCalledTimes(1);
 player.dispatchEvent(new Event('timeupdate'));await flush();expect(request).toHaveBeenCalledTimes(1);
 paused=false;player.dispatchEvent(new Event('playing'));
 await vi.advanceTimersByTimeAsync(1000);player.dispatchEvent(new Event('timeupdate'));await flush();expect(request).toHaveBeenCalledTimes(1);
 player.dispatchEvent(new Event('seeking'));player.currentTime=40;player.dispatchEvent(new Event('timeupdate'));await flush();expect(request).toHaveBeenCalledTimes(1);
});
it('records the first advancing play once',async()=>{
 await flush();paused=false;player.dispatchEvent(new Event('playing'));
 await vi.advanceTimersByTimeAsync(1000);player.currentTime=1;player.dispatchEvent(new Event('timeupdate'));await flush();
 expect(request.mock.calls.filter(c=>c[0].endsWith('playback-start'))).toHaveLength(1);
 await vi.advanceTimersByTimeAsync(1000);player.currentTime=2;player.dispatchEvent(new Event('timeupdate'));await flush();
 expect(request.mock.calls.filter(c=>c[0].endsWith('playback-start'))).toHaveLength(1);
});
it('renews while paused and preserves the playback position',async()=>{
 await flush();player.currentTime=91;await vi.advanceTimersByTimeAsync(240000);await flush();
 expect(player.currentTime).toBe(91);expect(player.play).not.toHaveBeenCalled();expect(player.src).toBe('https://storage.example.test/renewed');
});
it('resumes playing after a successful URL renewal',async()=>{
 await flush();paused=false;player.currentTime=93;await vi.advanceTimersByTimeAsync(240000);await flush();expect(player.currentTime).toBe(93);expect(player.play).toHaveBeenCalledTimes(1);
});
it('stops when renewed access is denied',async()=>{
 await flush();request.mockImplementation(async()=>new Response('{}',{status:403}));player.src='https://storage.example.test/old';paused=false;
 await vi.advanceTimersByTimeAsync(240000);await flush();expect(player.pause).toHaveBeenCalled();expect(player.hasAttribute('src')).toBe(false);
});
it('creates another playback session after the next playlist item starts',async()=>{
 await flush();paused=false;player.dispatchEvent(new Event('playing'));
 await vi.advanceTimersByTimeAsync(1000);player.currentTime=1;player.dispatchEvent(new Event('timeupdate'));await flush();
 player.dataset.shareToken='second';player.currentTime=0;player.dispatchEvent(new Event('timeupdate'));await flush();
 player.dispatchEvent(new Event('playing'));await vi.advanceTimersByTimeAsync(1000);player.currentTime=1;player.dispatchEvent(new Event('timeupdate'));await flush();
 expect(request.mock.calls.filter(c=>c[0].endsWith('playback-start')).map(c=>c[0])).toEqual(['/api/watch/synthetic/playback-start','/api/watch/second/playback-start']);
});
it('renews four times across a sixteen-minute paused playback interval',async()=>{
 await flush();player.currentTime=117;
 await vi.advanceTimersByTimeAsync(16*60*1000);await flush();
 expect(request.mock.calls.filter(c=>c[0].endsWith('/renew'))).toHaveLength(4);
 expect(player.currentTime).toBe(117);expect(player.play).not.toHaveBeenCalled();
});
