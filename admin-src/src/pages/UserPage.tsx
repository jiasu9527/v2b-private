import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, message } from 'antd';
import { apiGet, apiPost, bytes, unixTime } from '../lib/api';
import JsonModal from '../components/JsonModal';

const conditions = [{label:'模糊',value:'模糊'},{label:'等于',value:'='},{label:'大于',value:'>'},{label:'小于',value:'<'}];
const filterFields = [
  {label:'邮箱',value:'email'}, {label:'用户ID',value:'id'}, {label:'套餐ID',value:'plan_id'}, {label:'邀请人ID',value:'invite_user_id'},
  {label:'邀请码',value:'invite_code'}, {label:'最后在线',value:'last_login_at'}, {label:'状态',value:'banned'}, {label:'备注',value:'remarks'}
];

export default function UserPage() {
  const [rows,setRows]=useState<any[]>([]); const [total,setTotal]=useState(0); const [loading,setLoading]=useState(false);
  const [filter,setFilter]=useState({ key: 'email', condition: '模糊', value: '' });
  const [edit,setEdit]=useState<any>(null); const [detail,setDetail]=useState<any>(null); const [form]=Form.useForm();
  const load=async()=>{setLoading(true);try{const params:any={current:1,pageSize:50,sort:'id',sort_type:'DESC'}; if(filter.value.trim()){params.filter=[filter];} const r=await apiGet('/user/fetch',params);setRows(r.data||[]);setTotal(r.total||0);}catch(e:any){message.error(e.message||'加载失败')}finally{setLoading(false)}};
  useEffect(()=>{load()},[]);
  const save=async()=>{const v=await form.validateFields();await apiPost('/user/update',{id:edit.id,...v},{form:true});message.success('保存成功');setEdit(null);load();};
  const columns:any[]=[
    {title:'ID',dataIndex:'id',width:70},{title:'邮箱',dataIndex:'email',width:220},{title:'套餐',dataIndex:'plan_name',render:(v:any,r:any)=>v||r.plan_id||'-'},
    {title:'用户IP',dataIndex:'ips',width:220,render:(v:any,r:any)=>v||r.last_login_ip||'-'}, {title:'在线数',dataIndex:'alive_ip',width:90},
    {title:'已用',render:(_:any,r:any)=>bytes((Number(r.u)||0)+(Number(r.d)||0))},{title:'总量',dataIndex:'transfer_enable',render:bytes},
    {title:'最后在线',dataIndex:'last_login_at',render:unixTime},{title:'到期',dataIndex:'expired_at',render:unixTime},{title:'状态',dataIndex:'banned',render:(v:any)=><Tag color={v?'red':'green'}>{v?'封禁':'正常'}</Tag>},
    {title:'操作',fixed:'right',width:280,render:(_:any,r:any)=><Space wrap>
      <Button size="small" onClick={async()=>{const d=await apiGet('/user/getUserInfoById',{id:r.id});setDetail(d.data)}}>详情</Button>
      <Button size="small" onClick={()=>{setEdit(r);form.setFieldsValue(r)}}>编辑</Button>
      <Button size="small" onClick={async()=>{await apiPost('/user/resetSecret',{id:r.id});message.success('已重置')}}>重置密钥</Button>
      <Popconfirm title="确认删除用户？" onConfirm={async()=>{await apiPost('/user/delUser',{id:r.id});message.success('已删除');load();}}><Button size="small" danger>删除</Button></Popconfirm>
    </Space>}
  ];
  return <div className="page-stack"><Card title="用户管理" extra={<Space wrap>
    <Select value={filter.key} options={filterFields} style={{width:120}} onChange={key=>setFilter({...filter,key})}/>
    <Select value={filter.condition} options={conditions} style={{width:92}} onChange={condition=>setFilter({...filter,condition})}/>
    <Input.Search placeholder="过滤值" value={filter.value} onChange={e=>setFilter({...filter,value:e.target.value})} onSearch={load} style={{width:220}}/>
    <Button onClick={load}>刷新</Button>
  </Space>}><Table rowKey="id" loading={loading} columns={columns} dataSource={rows} pagination={{total,pageSize:50}} scroll={{x:'max-content'}}/></Card>
    <Modal title="编辑用户" open={!!edit} onCancel={()=>setEdit(null)} onOk={save}><Form form={form} layout="vertical"><Form.Item name="email" label="邮箱"><Input/></Form.Item><Form.Item name="transfer_enable" label="总流量(字节)"><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="expired_at" label="到期时间戳"><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="plan_id" label="套餐ID"><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="group_id" label="权限组ID"><InputNumber style={{width:'100%'}}/></Form.Item><Form.Item name="banned" label="封禁"><Select options={[{label:'正常',value:0},{label:'封禁',value:1}]}/></Form.Item></Form></Modal>
    <JsonModal open={!!detail} title="用户详情" data={detail} onClose={()=>setDetail(null)}/>
  </div>;
}
