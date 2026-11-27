import{c as e}from"./prefetch-CIoQFe54.js";import{r as t}from"./index-C1C4Tm-F.js";
/**
 * @license lucide-react v0.513.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const r=e("ban",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m4.9 4.9 14.2 14.2",key:"1m5liu"}]]),a=e("copy",[["rect",{width:"14",height:"14",x:"8",y:"8",rx:"2",ry:"2",key:"17jyea"}],["path",{d:"M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2",key:"zix9uf"}]]);function c(e=1500){const[r,a]=t.useState(null),c=t.useRef(null),u=t.useCallback(()=>{null!==c.current&&(clearTimeout(c.current),c.current=null)},[]);t.useEffect(()=>u,[u]);return{feedback:r,copy:t.useCallback(async(t,r)=>{u();try{await navigator.clipboard.writeText(t)}catch{return a({key:r,status:"failed"}),!1}return a({key:r,status:"ok"}),c.current=setTimeout(()=>{a(e=>e?.key===r&&"ok"===e.status?null:e),c.current=null},e),!0},[u,e]),clear:u}}export{r as B,a as C,c as u};
