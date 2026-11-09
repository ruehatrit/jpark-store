'use strict';
const PMTools=(()=>{
 const guidance=[['Guidance-B1_B15','10.10.36.28'],['Guidance-B1-B16','10.10.36.27'],['Guidance-B1-F8','10.10.36.21'],['Guidance-B1-M9','10.10.36.25'],['Guidance-B2-C12','10.10.36.22'],['Guidance-B2-F9','10.10.36.24'],['Guidance-B3-D12','10.10.36.23'],['Guidance-B3-N8','10.10.36.26']];
 const currentMonth=()=>{const parts=new Intl.DateTimeFormat('en-CA',{timeZone:'Asia/Bangkok',year:'numeric',month:'2-digit'}).formatToParts(new Date()),value=type=>parts.find(x=>x.type===type).value;return value('year')+'-'+value('month')};
 const isNVR=a=>[a.category,a.group,a.name,a.assetId].some(x=>String(x||'').toLowerCase().includes('nvr'));
 const key=a=>a.ip?'ip:'+a.ip:'asset:'+a.assetId+'-'+a.id;
 function makeItems(assets,cameras){const m=new Map();for(const a of assets||[]){if(a.active===false)continue;const k=key(a);m.set(k,{key:k,name:a.name||a.assetId||'อุปกรณ์',group:isNVR(a)?'NVR':String(a.category||'').toLowerCase().includes('guidance')?'Guidance':String(a.category||'').toLowerCase().includes('cctv')?'CCTV':'อุปกรณ์ลานจอด',ip:a.ip||'',location:a.location||'',isNVR:isNVR(a)})}for(const c of cameras||[]){const k=c.ip?'ip:'+c.ip:'cctv:channel-'+c.channel;if(!m.has(k))m.set(k,{key:k,name:c.name||'CCTV CH '+c.channel,group:'CCTV',ip:c.ip||'',location:'',isNVR:false})}for(const [name,ip]of guidance){const k='ip:'+ip;if(!m.has(k))m.set(k,{key:k,name,group:'Guidance',ip,location:'',isNVR:false})}return [...m.values()].sort((a,b)=>Number(b.isNVR)-Number(a.isNVR)||a.name.localeCompare(b.name,'th',{numeric:true}))}
 const inspectors=r=>[...new Set((Array.isArray(r?.inspectors)&&r.inspectors.length?r.inspectors:String(r?.inspector||'').split(' / ')).map(s=>s.trim()).filter(Boolean))];
 const csvCell=x=>'"'+String(x??'').replace(/^[=+@\-\t\r]/,"'$&").replace(/"/g,'""')+'"';
 return {guidance,currentMonth,isNVR,key,makeItems,inspectors,csvCell};
})();
