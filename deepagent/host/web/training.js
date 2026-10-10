// 复用现有消息 ID、训练样本和状态接口；不另建消息或训练流程。
let trainingEnabled = false, trainingExamples = [], trainingStatus = {};
let trainingSource = null, trainingPreviewRequest = 0, trainingDataRequest = 0;

async function loadTrainingData() {
  const request = ++trainingDataRequest;
  const status = await api('/api/local-model/status');
  const examples = await api('/api/local-model/examples');
  if (request !== trainingDataRequest) return;
  trainingEnabled = !!status.enabled; trainingStatus = status; trainingExamples = examples;
  $('tabTraining').hidden = !trainingEnabled;
  if (trainingEnabled && !panelNames.includes('Training')) panelNames.push('Training');
  messagesByKey.forEach(renderTrainingButton); renderTrainingData();
}
function renderTrainingButton(record) {
  if (!trainingEnabled || !record.finished || !record.messageID || !record.trainingEligible) return;
  let button = record.trainingButton;
  if (!button) {
    button = document.createElement('button'); button.type = 'button'; button.className = 'training-action';
    record.element.append(button); record.trainingButton = button;
  }
  const marked = trainingExamples.some(example => example.source_message_ids?.includes(record.messageID));
  setLabel(button, marked ? '✓ 已加入训练 · 取消标记' : '用于训练');
  button.onclick = () => marked ? removeTrainingExample(record) : openTrainingPreview(record);
}
function renderTrainingData() {
  const count = trainingStatus.new_example_count || 0, requiredCount = trainingStatus.required_example_count;
  const latestJob = trainingStatus.jobs?.[0];
  const state = latestJob?.status === 'running' ? '正在训练，当前任务继续使用 API 模型' : !trainingStatus.auto_training_enabled ? '自动训练未启用' : count >= requiredCount ? '样本已够 · 等待满足空闲和接电条件' : '积累样本中';
  setLabel($('trainingState'),state);
  setLabel($('trainingCount'),'新增确认样本：{count} / {requiredCount}',{count,requiredCount});
  setLabel($('trainingConditions'),'自动开始条件：新增 {count} 条确认样本、无执行中任务、空闲 10 分钟、已接电。',{count:requiredCount});
  delete $('trainingExamples').dataset.i18n;
  $('trainingExamples').replaceChildren();
  messagesByKey.forEach(record => {
    if (!record.messageID || record.threadID !== thread) return;
    const example = trainingExamples.find(item => item.source_message_ids?.includes(record.messageID));
    if (!example) return;
    const row = document.createElement('article'); row.className = 'training-example';
    const question = document.createElement('p'), answer = document.createElement('p');
    question.textContent = example.messages.at(-2).content; answer.textContent = example.messages.at(-1).content;
    const source = document.createElement('button'); source.type = 'button'; source.className = 'training-action'; setLabel(source,'回到原消息');
    source.onclick = () => { showPanel('Talk'); record.element.scrollIntoView({block:'center'}); record.element.classList.add('training-highlight'); setTimeout(() => record.element.classList.remove('training-highlight'),1500); };
    const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'training-action'; setLabel(remove,'移出训练');
    remove.onclick = () => removeTrainingExample(record);
    row.append(question,answer,source,remove); $('trainingExamples').append(row);
  });
  if (!$('trainingExamples').children.length) setLabel($('trainingExamples'),'还没有标记。在已完成的回复下选择“用于训练”。');
  delete $('trainingJobs').dataset.i18n;
  $('trainingJobs').replaceChildren();
  const labels = {running:'训练中',pending_validation:'参数待验证，尚未启用',failed:'训练失败',canceled:'训练已取消'};
  (trainingStatus.jobs || []).slice(0,5).forEach(job => {
    const row = document.createElement('div'); row.className = 'training-example';
    const status = document.createElement('span'); setLabel(status,labels[job.status] || job.status);
    const time = document.createElement('small'); time.textContent = new Date(job.created_at).toLocaleString();
    row.append(status,time);
    if (job.error) { const error = document.createElement('p'); error.textContent = job.error; row.append(error); }
    $('trainingJobs').append(row);
  });
  if (!$('trainingJobs').children.length) setLabel($('trainingJobs'),'尚未开始训练');
}
async function openTrainingPreview(record) {
  trainingSource = {threadID:record.threadID,messageID:record.messageID};
  $('trainingIncludePrevious').checked = false;
  $('trainingReview').hidden = false; $('trainingData').hidden = true;
  $('trainingAnswer').value = ''; $('trainingQuestion').replaceChildren();
  showPanel('Training');
  await loadTrainingPreview(true);
}
async function loadTrainingPreview(resetAnswer = false) {
  const current = generation, request = ++trainingPreviewRequest, source = trainingSource;
  if (!source) return;
  $('confirmTraining').disabled = true;
  if (resetAnswer) $('trainingIncludePrevious').disabled = true;
  setLabel($('trainingNotice'),'正在读取…');
  try {
    const messages = await api('/api/local-model/examples?thread_id=' + source.threadID + '&message_id=' + source.messageID + '&include_previous=' + $('trainingIncludePrevious').checked);
    if (current !== generation || request !== trainingPreviewRequest || source !== trainingSource) return;
    $('trainingQuestion').replaceChildren();
    messages.slice(0,-1).forEach(message => {
      const row = document.createElement('div'); row.className = 'training-example';
      const role = document.createElement('small'); setLabel(role,message.role === 'user' ? '你' : '搭档');
      const text = document.createElement('p'); text.textContent = message.content; row.append(role,text); $('trainingQuestion').append(row);
    });
    if (resetAnswer) $('trainingAnswer').value = messages.at(-1).content;
    $('confirmTraining').disabled = false; $('trainingIncludePrevious').disabled = false; setLabel($('trainingNotice'),'');
  } catch (error) {
    if (current === generation && request === trainingPreviewRequest) { delete $('trainingNotice').dataset.i18n; $('trainingNotice').textContent = error.message; }
  }
}
async function confirmTrainingExample() {
  const source = trainingSource, current = generation;
  const answer = $('trainingAnswer').value.trim();
  if (!source || !answer || $('confirmTraining').disabled) return;
  $('confirmTraining').disabled = true;
  try {
    await api('/api/local-model/examples',{method:'POST',body:JSON.stringify({thread_id:source.threadID,message_id:source.messageID,include_previous:$('trainingIncludePrevious').checked,answer,confirmed:true})});
    await loadTrainingData();
    if (current !== generation || source !== trainingSource) return;
    $('trainingReview').hidden = true; $('trainingData').hidden = false; trainingSource = null;
    setLabel($('trainingNotice'),'已保存训练副本，原始对话保持不变。'); showPanel('Talk');
  } catch (error) {
    if (current === generation && source === trainingSource) { delete $('trainingNotice').dataset.i18n; $('trainingNotice').textContent = error.message; }
  } finally {
    if (current === generation && source === trainingSource) $('confirmTraining').disabled = false;
  }
}
async function removeTrainingExample(record) {
  const current = generation, source = trainingSource;
  try {
    await api('/api/local-model/examples?thread_id=' + record.threadID + '&message_id=' + record.messageID,{method:'DELETE'});
    await loadTrainingData();
    if (current === generation && source === trainingSource) setLabel($('trainingNotice'),'已移出后续训练；已生成的模型参数不会因此回退。');
  } catch (error) {
    if (current !== generation || source !== trainingSource) return;
    delete $('trainingNotice').dataset.i18n; $('trainingNotice').textContent = error.message; showPanel('Training');
  }
}
function initializeTraining() {
  $('tabTraining').hidden = true;
  $('tabTraining').onclick = () => { trainingSource = null; setLabel($('trainingNotice'),''); $('trainingReview').hidden = true; $('trainingData').hidden = false; showPanel('Training'); loadTrainingData().catch(error => { $('trainingNotice').textContent = error.message; }); };
  $('backTraining').onclick = () => { trainingSource = null; showPanel('Talk'); };
  $('trainingIncludePrevious').onchange = () => loadTrainingPreview();
  $('confirmTraining').onclick = confirmTrainingExample;
  loadTrainingData().catch(error => { if (error.status !== 404) console.warn('Local model status:',error.message); });
}
