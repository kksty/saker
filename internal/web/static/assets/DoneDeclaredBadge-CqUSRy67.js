import{c as e}from"./prefetch-D7pkws_c.js";import{j as n}from"./index-BozBXurh.js";import{B as t}from"./badge-Bcn6gawl.js";import{a as r}from"./format-C20eUcxs.js";
/**
 * @license lucide-react v0.513.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const s=e("crosshair",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["line",{x1:"22",x2:"18",y1:"12",y2:"12",key:"l9bcsi"}],["line",{x1:"6",x2:"2",y1:"12",y2:"12",key:"13hhkx"}],["line",{x1:"12",x2:"12",y1:"6",y2:"2",key:"10w3f3"}],["line",{x1:"12",x2:"12",y1:"22",y2:"18",key:"15g9kq"}]]);function o(e){switch(e){case"commander":return"指挥官判定";case"operator":return"操作员判定";default:return e?"判定完成":""}}function a(e){const n=[];return e.done_note&&n.push(`说明：${e.done_note}`),e.done_fact_ids?.length&&n.push(`证据事实：${e.done_fact_ids.join("，")}`),e.done_run_ids?.length&&n.push(`证据运行：${e.done_run_ids.join("，")}`),e.done_at&&n.push(`判定于 ${r(e.done_at)}`),n.join("\n")}function i({intent:e,className:r}){if(!e?.done_declared_by||e.status&&"done"!==e.status)return null;const s=function(e){switch(e){case"commander":return"指挥官判定完成";case"operator":return"操作员判定完成";default:return e?"判定完成":""}}(e.done_declared_by),o=a(e);return n.jsx(t,{size:"sm",variant:"outline",className:r,title:o||s,children:s})}export{s as C,i as D,a,o as d};
