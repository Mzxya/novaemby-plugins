"use strict";
(() => {
  const key="novaemby.playback-history.theme";
  function apply(value) {
    const theme=value==="dark"?"dark":"light";
    document.documentElement.dataset.theme=theme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content",theme==="dark"?"#14151d":"#f7f8fc");
    const button=document.getElementById("theme");if(button){button.setAttribute("aria-pressed",String(theme==="dark"));button.setAttribute("aria-label",theme==="dark"?"切换到亮色主题":"切换到暗色主题");}
  }
  let saved="light";try{saved=window.localStorage.getItem(key);}catch{}apply(saved);
  window.HistoryTheme={sync:()=>apply(document.documentElement.dataset.theme),toggle(){const value=document.documentElement.dataset.theme==="dark"?"light":"dark";apply(value);try{window.localStorage.setItem(key,value);}catch{}}};
  window.addEventListener("storage",event=>{if(event.key===key||event.key===null)apply(event.newValue);});
})();
