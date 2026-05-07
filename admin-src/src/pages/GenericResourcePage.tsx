import React from 'react';
import CrudPage from '../components/CrudPage';
import { resources } from '../lib/resources';
export default function GenericResourcePage({ name }: { name: keyof typeof resources }) { return <CrudPage config={resources[name]} />; }
